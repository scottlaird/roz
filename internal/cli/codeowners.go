package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/codeowners"
	"github.com/scottlaird/roz/internal/github"
)

const (
	flagOwnersFile = "owners"
	flagApproved   = "approved"
	flagPath       = "path"
	flagTeam       = "team"
	flagForPR      = "pr"
)

func newCodeownersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "codeowners",
		Short: "Work out who has to approve a set of changed files",
		Long: "Reads a CODEOWNERS file and a list of paths, and answers the two\n" +
			"questions a list of owners cannot: whether one approval could cover\n" +
			"the whole change, and — once somebody has approved — which of the\n" +
			"remaining owners would actually move it forward.\n\n" +
			"Paths come from stdin, one per line, which is what `gh` already\n" +
			"produces:\n\n" +
			"  gh pr diff 123 --name-only | roz codeowners --owners CODEOWNERS\n\n" +
			"--path takes them as arguments instead, which is how anything without\n" +
			"a shell has to pass them.\n\n" +
			"--approved takes owners, not reviewers, because resolving a login to\n" +
			"the teams it approves for needs the GitHub API. --team supplies that\n" +
			"mapping by hand where it matters: --team org/platform=alice,bob makes\n" +
			"--approved alice satisfy the team as well as the person.\n\n" +
			"--pr fetches all three from GitHub instead — the changed files, the\n" +
			"CODEOWNERS on the base branch, and who has already approved — which is\n" +
			"the whole question in one command:\n\n" +
			"  roz codeowners --pr owner/repo#812\n\n" +
			"Nothing here reads the database. It is the library behind review\n" +
			"routing, exposed so it can be pointed at a real change today.",
		Args: cobra.NoArgs,
		RunE: runCodeowners,
	}
	f := cmd.Flags()
	f.String(flagOwnersFile, "CODEOWNERS", "path to the CODEOWNERS file")
	f.StringSlice(flagApproved, nil, "owners or reviewers who have already approved")
	f.StringArray(flagTeam, nil, "team membership, as org/team=login,login; repeatable")
	// stdin is the natural way to pipe a file list, and the one way an agent
	// calling this over MCP cannot use.
	f.StringArray(flagPath, nil, "a changed path; repeatable, and read from stdin when not given")
	f.String(flagForPR, "", "read the files, CODEOWNERS and approvals from a pull request, e.g. owner/repo#812")
	return cmd
}

func runCodeowners(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()

	text, source, paths, approvedBy, err := changeUnderReview(cmd)
	if err != nil {
		return err
	}

	owners, err := codeowners.ParseString(text)
	if err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}

	teams, err := teamsFrom(cmd)
	if err != nil {
		return err
	}
	// A pull request can say who is in which team; a local file cannot, so
	// --team stays the answer there.
	if f.Changed(flagForPR) && !f.Changed(flagTeam) {
		teams = resolveTeams(cmd, owners, teams)
	}
	// --approved adds to whatever the pull request already reports, rather
	// than replacing it: naming somebody by hand should not quietly discard
	// the approvals GitHub knows about.
	given, err := f.GetStringSlice(flagApproved)
	if err != nil {
		return err
	}
	approvedBy = append(approvedBy, given...)

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "owners  %s\n", source)
	return reportOwnership(out, owners.Of(paths),
		codeowners.Approval(approvedBy, teams), len(approvedBy) > 0)
}

// changeUnderReview resolves what to reason about: a pull request GitHub can
// describe, or a file and a list of paths supplied by hand.
func changeUnderReview(cmd *cobra.Command) (text, source string, paths, approved []string, err error) {
	f := cmd.Flags()

	key, err := f.GetString(flagForPR)
	if err != nil {
		return "", "", nil, nil, err
	}

	if key != "" {
		if f.Changed(flagPath) {
			return "", "", nil, nil,
				fmt.Errorf("--%s brings its own file list; drop --%s", flagForPR, flagPath)
		}
		change, err := newChangeReader().Change(cmd.Context(), key)
		if err != nil {
			return "", "", nil, nil, err
		}
		if change.Codeowners == "" {
			return "", "", nil, nil, fmt.Errorf(
				"%s has no CODEOWNERS on %s, so nobody in particular is required",
				key, change.BaseRef)
		}
		// Who has already approved is part of the answer, not something to go
		// and look up and type back in.
		return change.Codeowners,
			fmt.Sprintf("%s@%s", change.CodeownersPath, change.BaseRef),
			change.Files, change.Approvals, nil
	}

	path, err := f.GetString(flagOwnersFile)
	if err != nil {
		return "", "", nil, nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("reading %s: %w", path, err)
	}

	if paths, err = f.GetStringArray(flagPath); err != nil {
		return "", "", nil, nil, err
	}
	if len(paths) == 0 {
		if paths, err = readPaths(cmd.InOrStdin()); err != nil {
			return "", "", nil, nil, err
		}
	}
	if len(paths) == 0 {
		return "", "", nil, nil, fmt.Errorf(
			"no paths: pass --%s or --%s, or pipe a file list, e.g. `gh pr diff N --name-only`",
			flagForPR, flagPath)
	}
	return string(raw), path, paths, nil, nil
}

// newChangeReader builds the GitHub client. A test replaces it, the way
// newFetcher is replaced for sync.
var newChangeReader = func() changeReader { return github.New() }

// changeReader is what this needs from GitHub, named so a test can stand in
// for it without a network.
type changeReader interface {
	Change(ctx context.Context, key string) (github.Change, error)
	TeamMembers(ctx context.Context, teams []string) (map[string][]string, error)
}

// resolveTeams fills in team membership from GitHub, so a review by a person
// satisfies the teams they are in.
//
// Only reachable with --pr, since without a pull request there is no client
// call to make and --team is the whole answer. Only asked about the teams the
// file names, which is why File.Teams exists.
//
// A failure here is reported and not propagated: the rest of the answer — which
// owners exist, what is unowned, whether one owner covers everything — is
// correct without it, and refusing to print any of that because one enrichment
// failed would be the wrong trade. It is said out loud rather than absorbed,
// since silently under-resolving membership makes a change look less approved
// than it is. A token without read:org is the common way to land here.
func resolveTeams(cmd *cobra.Command, owners *codeowners.File, byHand codeowners.Teams) codeowners.Teams {
	named := owners.Teams()
	if len(named) == 0 {
		return byHand
	}
	refs := make([]string, len(named))
	for i, team := range named {
		refs[i] = string(team)
	}

	members, err := newChangeReader().TeamMembers(cmd.Context(), refs)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"note: could not read team membership, so an approval only satisfies "+
				"the person who gave it; pass --%s to supply it: %v\n", flagTeam, err)
		return byHand
	}
	return codeowners.NewStaticTeams(members)
}

// readPaths takes the file list off stdin, one per line.
func readPaths(in io.Reader) ([]string, error) {
	var paths []string
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			paths = append(paths, line)
		}
	}
	return paths, scanner.Err()
}

// teamsFrom reads the --team flags into a membership table.
func teamsFrom(cmd *cobra.Command) (codeowners.Teams, error) {
	given, err := cmd.Flags().GetStringArray(flagTeam)
	if err != nil {
		return nil, err
	}
	if len(given) == 0 {
		return codeowners.NoTeams{}, nil
	}

	members := map[string][]string{}
	for _, entry := range given {
		team, logins, found := strings.Cut(entry, "=")
		if !found || strings.TrimSpace(team) == "" || strings.TrimSpace(logins) == "" {
			return nil, fmt.Errorf("--%s %q is not team=login,login", flagTeam, entry)
		}
		members[team] = append(members[team], strings.Split(logins, ",")...)
	}
	return codeowners.NewStaticTeams(members), nil
}

func reportOwnership(out io.Writer, o *codeowners.Ownership,
	approved codeowners.OwnerSet, anyApproved bool) error {

	remaining := o.Remaining(approved)
	unowned := o.Unowned()
	// Nothing owned at all is its own answer, and a common one against a
	// CODEOWNERS that covers a few specific paths in a large repository.
	// Reporting "no single owner covers every file" there is true and
	// misleading: it reads as a problem, and sits oddly beside the "nothing
	// outstanding" that follows.
	nobodyRequired := len(unowned) == len(o.Files)

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "files\t%d\n", len(o.Files))
	if len(unowned) > 0 {
		fmt.Fprintf(w, "unowned\t%d\n", len(unowned))
	}

	switch {
	case nobodyRequired:
		fmt.Fprintf(w, "required\t-\tno rule matches any of these files\n")
	// The cheap answer next: it is often the only one needed.
	default:
		if sole := o.SoleApprovers(); len(sole) > 0 {
			fmt.Fprintf(w, "any one of\t%s\n", joinOwners(sole))
		} else if !anyApproved {
			fmt.Fprintf(w, "any one of\t-\tno single owner covers every file\n")
		}
	}

	if anyApproved {
		fmt.Fprintf(w, "approved as\t%s\n", joinOwners(approved.Sorted()))
		fmt.Fprintf(w, "outstanding\t%d\n", len(remaining))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if len(remaining) == 0 {
		if !nobodyRequired {
			fmt.Fprintln(out, "\nnothing outstanding")
		}
		return nil
	}

	// Who is worth asking, and what each would buy. This is the part that a
	// list of the owners a change mentions cannot tell you.
	fmt.Fprintln(out, "\nwould cover")
	w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, c := range o.Useful(approved) {
		fmt.Fprintf(w, "  %s\t%d of %d\n", c.Owner, c.Files, len(remaining))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if plan := o.Plan(approved); len(plan) > 1 {
		fmt.Fprintf(out, "\nfewest approvals: %s\n", joinOwners(plan))
	}
	return nil
}

func joinOwners(owners []codeowners.Owner) string {
	out := make([]string, len(owners))
	for i, owner := range owners {
		out[i] = owner.String()
	}
	return strings.Join(out, " ")
}
