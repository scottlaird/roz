package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/codeowners"
	"github.com/scottlaird/roz/internal/github"
	"github.com/scottlaird/roz/internal/reviewqueue"
	"github.com/scottlaird/roz/internal/store"
)

const (
	flagQueueRepo          = "repo"
	flagQueueTeam          = "team"
	flagQueueMaxRuleOwners = "max-rule-owners"
	flagQueueShowSkipped   = "show-skipped"
	flagQueuePost          = "post"
	flagQueueStaleAfter    = "stale-after"
	flagQueueVerbose       = "verbose"
	flagQueueFormat        = "format"
	flagQueueShowReasons   = "show-reasons"
	flagQueueJiraURL       = "jira-url"
	flagQueueTable         = "table"
	flagQueuePageSize      = "page-size"
	flagQueueIgnoreTeam    = "ignore-team"
	flagQueueSLOWarn       = "slo-warn"
	flagQueueSLOBreach     = "slo-breach"

	// envSlackWebhook is where --post finds its Slack incoming webhook. An
	// environment variable rather than a flag, so the URL stays out of shell
	// history and process listings.
	envSlackWebhook = "ROZ_SLACK_WEBHOOK_URL"
)

func newReviewQueueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review-queue",
		Short: "List the open pull requests waiting on a team",
		Long: "Reads a repository's open pull requests straight from GitHub and\n" +
			"lists those that are a team's to review, with who each is waiting on.\n\n" +
			"A pull request is the team's when its current diff touches a file whose\n" +
			"CODEOWNERS rule names the team, or when a team member is requested or\n" +
			"assigned by name. Drafts and approved pull requests are left out: neither\n" +
			"is waiting on a reviewer. Review requests alone don't count: GitHub never takes\n" +
			"back a CODEOWNERS request when the files that caused it leave the diff.\n" +
			"Rules naming more than --max-rule-owners owners are catch-alls and don't\n" +
			"count either.\n\n" +
			"A pull request stacked on others -- its base branch is another open pull\n" +
			"request's head -- is held back, listed on one line at the end, until every\n" +
			"pull request under it is approved.\n\n" +
			"A pull request can carry review requests that no longer apply: GitHub never\n" +
			"withdraws one. --ignore-team leaves a team out of \"waiting on\" everywhere,\n" +
			"for a gate that has been retired.\n\n" +
			"Jira keys in titles, and issue links in descriptions, are linked using\n" +
			"roz's jira_base_url and limited to its jira_prefixes, as on the pages.\n\n" +
			"Prints Slack mrkdwn. --post sends it to the incoming webhook in\n" +
			envSlackWebhook + " instead. Reads roz's config if there is a database,\n" +
			"and writes nothing to it.",
		Example: "  roz review-queue --team org/storage --repo org/service\n" +
			"  roz review-queue --team org/storage --repo org/service --show-skipped",
		RunE: runReviewQueue,
	}
	f := cmd.Flags()
	f.StringSlice(flagQueueRepo, nil, "repository to scan, owner/name (repeatable, required)")
	f.String(flagQueueTeam, "", "team whose queue this is, org/slug (required)")
	f.Int(flagQueueMaxRuleOwners, 3, "ignore CODEOWNERS rules naming more owners than this; 0 means no limit")
	f.Bool(flagQueueShowSkipped, false, "also list pull requests the team is requested on but that aren't its, and why")
	f.Bool(flagQueuePost, false, "post to the Slack webhook in "+envSlackWebhook+" rather than printing")
	f.Duration(flagQueueStaleAfter, 60*24*time.Hour, "fold pull requests idle longer than this into one line; 0 folds nothing")
	f.BoolP(flagQueueVerbose, "v", false, "report progress on stderr")
	f.Bool(flagQueueShowReasons, false, "say why each pull request is the team's, for checking the filter")
	f.String(flagQueueJiraURL, "", "Jira base to link issue keys to, e.g. https://example.atlassian.net/browse (default: roz's jira_base_url)")
	f.String(flagQueueTable, "simple", `with --format blocks: "simple" tables, or "data" for Slack's paginated, sortable data tables`)
	f.Int(flagQueuePageSize, 10, "rows per page in a data table")
	f.StringSlice(flagQueueIgnoreTeam, nil, "team never to show as waited on, org/slug (repeatable)")
	f.Duration(flagQueueSLOWarn, 48*time.Hour, "mark pull requests idle longer than this with a yellow circle; 0 marks none")
	f.Duration(flagQueueSLOBreach, 7*24*time.Hour, "mark pull requests idle longer than this with a red circle; 0 marks none")
	f.String(flagQueueFormat, "mrkdwn", `"mrkdwn" for a text message, or "blocks" for a Block Kit payload with tables (paste into Block Kit Builder to preview)`)
	_ = cmd.MarkFlagRequired(flagQueueRepo)
	_ = cmd.MarkFlagRequired(flagQueueTeam)
	return cmd
}

func runReviewQueue(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()
	repos, _ := f.GetStringSlice(flagQueueRepo)
	teamRef, _ := f.GetString(flagQueueTeam)
	maxRuleOwners, _ := f.GetInt(flagQueueMaxRuleOwners)
	showSkipped, _ := f.GetBool(flagQueueShowSkipped)
	post, _ := f.GetBool(flagQueuePost)
	staleAfter, _ := f.GetDuration(flagQueueStaleAfter)
	verbose, _ := f.GetBool(flagQueueVerbose)
	format, _ := f.GetString(flagQueueFormat)
	showReasons, _ := f.GetBool(flagQueueShowReasons)
	jiraURL, _ := f.GetString(flagQueueJiraURL)
	table, _ := f.GetString(flagQueueTable)
	pageSize, _ := f.GetInt(flagQueuePageSize)
	ignoreRefs, _ := f.GetStringSlice(flagQueueIgnoreTeam)
	sloWarn, _ := f.GetDuration(flagQueueSLOWarn)
	sloBreach, _ := f.GetDuration(flagQueueSLOBreach)
	if table != "simple" && table != "data" {
		return fmt.Errorf(`--table must be "simple" or "data", not %q`, table)
	}
	if format != "mrkdwn" && format != "blocks" {
		return fmt.Errorf(`--format must be "mrkdwn" or "blocks", not %q`, format)
	}
	logf := func(format string, args ...any) {
		if verbose {
			fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", args...)
		}
	}

	team := codeowners.NormalizeOwner(teamRef)
	if !team.IsTeam() {
		return fmt.Errorf("--team %q is not org/slug", teamRef)
	}
	var ignore []codeowners.Owner
	for _, ref := range ignoreRefs {
		o := codeowners.NormalizeOwner(ref)
		if !o.IsTeam() {
			return fmt.Errorf("--%s %q is not org/slug", flagQueueIgnoreTeam, ref)
		}
		ignore = append(ignore, o)
	}
	webhook := os.Getenv(envSlackWebhook)
	if post && webhook == "" {
		return fmt.Errorf("--post needs a Slack incoming webhook URL in %s", envSlackWebhook)
	}

	jira, err := jiraSettings(cmd, jiraURL)
	if err != nil {
		return err
	}
	logf("Jira: base %q, projects %v", jira.base, jira.prefixes)

	ctx := cmd.Context()
	gh := github.New()

	logf("reading %s's members", team)
	members, err := gh.TeamMembers(ctx, []string{string(team)})
	if err != nil {
		return fmt.Errorf("reading %s's members: %w", team, err)
	}
	logf("%s has %d members", team, len(members[string(team)]))
	cfg := reviewqueue.Config{
		Team: team, Members: members[string(team)], MaxRuleOwners: maxRuleOwners,
		JiraPrefixes: jira.prefixes, IgnoreTeams: ignore,
	}

	var message, diagnostics strings.Builder
	var blocks []any
	for _, repo := range repos {
		logf("%s: listing open pull requests", repo)
		prs, err := gh.OpenPullRequests(ctx, repo, func(read int) {
			logf("%s: %d read", repo, read)
		})
		if err != nil {
			return err
		}
		owners := map[string]*codeowners.File{}
		for _, pr := range prs {
			if _, seen := owners[pr.BaseRef]; seen || pr.Draft {
				continue
			}
			logf("%s: reading CODEOWNERS on %s", repo, pr.BaseRef)
			text, _, err := gh.Codeowners(ctx, repo, pr.BaseRef)
			if err != nil {
				return fmt.Errorf("reading %s CODEOWNERS on %s: %w", repo, pr.BaseRef, err)
			}
			file, err := codeowners.ParseString(text)
			if err != nil {
				return fmt.Errorf("parsing %s CODEOWNERS on %s: %w", repo, pr.BaseRef, err)
			}
			owners[pr.BaseRef] = file
		}

		logf("%s: listing open pull request branches, for stacks", repo)
		branches, err := gh.OpenPRBranches(ctx, repo)
		if err != nil {
			return err
		}
		// Membership of every team the CODEOWNERS files name, so the report
		// can tell when one approval satisfies several teams.
		teamSet := map[string]bool{string(team): true}
		for _, file := range owners {
			for _, t := range file.Teams() {
				teamSet[string(t)] = true
			}
		}
		refs := make([]string, 0, len(teamSet))
		for t := range teamSet {
			refs = append(refs, t)
		}
		sort.Strings(refs)
		logf("%s: reading membership of %d teams", repo, len(refs))
		teamMembers, err := gh.TeamMembers(ctx, refs)
		if err != nil {
			return fmt.Errorf("reading team membership: %w", err)
		}

		repoCfg := cfg
		repoCfg.Branches = branches
		repoCfg.TeamMembers = teamMembers

		items, skipped := reviewqueue.Select(prs, owners, repoCfg)
		logf("%s: %d for %s, %d requested but not its", repo, len(items), team, len(skipped))
		opts := reviewqueue.Options{
			Now: time.Now(), StaleAfter: staleAfter, ShowReasons: showReasons, JiraURL: jira.base,
			DataTables: table == "data", PageSize: pageSize,
			SLOWarn: sloWarn, SLOBreach: sloBreach,
		}
		message.WriteString(reviewqueue.Format(repo, team, items, opts))
		payload := reviewqueue.Blocks(repo, team, items, opts)
		blocks = append(blocks, payload["blocks"].([]any)...)
		if showSkipped && len(skipped) > 0 {
			fmt.Fprintf(&diagnostics, "%s: %d requested but not the team's\n", repo, len(skipped))
			diagnostics.WriteString(reviewqueue.FormatSkipped(skipped))
		}
	}

	// A payload with blocks still needs text: it is what notifications and
	// clients that can't draw the blocks show.
	payload := map[string]any{"text": message.String(), "unfurl_links": false}
	if format == "blocks" {
		payload["text"] = fmt.Sprintf("Pull requests waiting on %s", team.String())
		payload["blocks"] = blocks
	}

	out := cmd.OutOrStdout()
	switch {
	case post:
		if err := postToSlack(webhook, payload); err != nil {
			return err
		}
		fmt.Fprintln(out, "posted")
	case format == "blocks":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{"blocks": blocks}); err != nil {
			return err
		}
	default:
		fmt.Fprint(out, message.String())
	}
	if diagnostics.Len() > 0 {
		fmt.Fprint(cmd.ErrOrStderr(), diagnostics.String())
	}
	return nil
}

type jiraConfig struct {
	base     string
	prefixes []string
}

// jiraSettings resolves where Jira keys link and which projects count, the
// way the rendered pages do: roz's config, then ROZ_JIRA_BASE_URL and
// ROZ_JIRA_PREFIXES, then --jira-url. A database that isn't there is not an
// error -- the queue needs nothing else from it -- it just means no config.
func jiraSettings(cmd *cobra.Command, override string) (jiraConfig, error) {
	var resolved jiraConfig
	if st, err := openStore(cmd); err == nil {
		defer st.Close()
		s, err := pageSettings(cmd.Context(), st)
		if err != nil {
			return jiraConfig{}, err
		}
		resolved = jiraConfig{base: s.jiraBase, prefixes: s.jiraPrefixes}
	} else {
		if v := os.Getenv("ROZ_JIRA_BASE_URL"); v != "" {
			resolved.base = v
		}
		if v := os.Getenv("ROZ_JIRA_PREFIXES"); v != "" {
			resolved.prefixes = splitPrefixes(v)
		}
	}
	if override != "" {
		if err := store.ValidateJiraBaseURL(override); err != nil {
			return jiraConfig{}, err
		}
		resolved.base = override
	}
	return resolved, nil
}

// postToSlack sends a message payload to an incoming webhook.
func postToSlack(webhook string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := http.Post(webhook, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("posting to Slack: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		reply, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("posting to Slack: %s: %s", resp.Status, strings.TrimSpace(string(reply)))
	}
	return nil
}
