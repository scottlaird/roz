package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/ghsync"
	"github.com/scottlaird/roz/internal/github"
	"github.com/scottlaird/roz/internal/store"
)

// syncSources are the external systems this build can read.
//
// jira and slack are named in the design but not implemented, and are listed
// so the error says so rather than "unknown source".
var syncSources = map[string]bool{"github": true}

var plannedSources = map[string]bool{"jira": true, "slack": true}

// newFetcher builds the GitHub client. A test replaces it.
var newFetcher = func() ghsync.Fetcher { return github.New() }

func newSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync <source>",
		Short: "Refresh observed fields from an external system",
		Long: "Reads only. Nothing here writes to GitHub, and the actor is sync:github,\n" +
			"so the store refuses any attempt to touch an authored column.\n\n" +
			"Pull requests are polled in batches through one GraphQL query each, so\n" +
			"the cost is a handful of rate limit points however many are tracked.\n" +
			"Only pull requests already tracked with `roz pr track` are polled.\n\n" +
			"Issues a project tracks through `roz project link-issue --tracker github`\n" +
			"are read the same way: title, state, milestone and assignees.",
		Args: cobra.ExactArgs(1),
		RunE: runSync,
	}
	cmd.Flags().Bool("quiet", false, "print only what changed")
	return cmd
}

func runSync(cmd *cobra.Command, args []string) error {
	source := args[0]
	switch {
	case syncSources[source]:
	case plannedSources[source]:
		return fmt.Errorf("syncing %s is designed but not built yet", source)
	default:
		return fmt.Errorf("%q is not a source: use github", source)
	}

	quiet, err := cmd.Flags().GetBool("quiet")
	if err != nil {
		return err
	}
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	result, err := ghsync.Sync(cmd.Context(), st, newFetcher())
	if err != nil {
		return err
	}
	return reportSync(cmd, result, quiet)
}

func reportSync(cmd *cobra.Command, result ghsync.Result, quiet bool) error {
	out := cmd.OutOrStdout()

	for _, key := range sortedKeys(result.Changed) {
		for _, change := range result.Changed[key] {
			fmt.Fprintf(out, "%s %s\n", key, change)
		}
	}
	for _, key := range sortedKeys(result.Missing) {
		fmt.Fprintf(out, "%s could not be read: %s\n", key, result.Missing[key])
	}
	for _, key := range sortedKeys(result.Backfilled) {
		fmt.Fprintf(out, "%s: %d recorded on the first poll\n", key, result.Backfilled[key])
	}
	for _, issue := range result.Issues {
		for _, change := range issue.Changes {
			fmt.Fprintf(out, "%s %s\n", issue.Key, change)
		}
	}
	for _, note := range result.IssuesClosedWithWork {
		fmt.Fprintf(out, "%s\n", note)
	}
	for _, o := range result.Owners {
		fmt.Fprintf(out, "%s needs %s\n", o.PR, ownersText(o.Required))
	}
	for _, r := range result.Resolved {
		fmt.Fprintf(out, "%s waits for %s (%s)\n", r.ActionID, r.Wait.Spec(), r.Spec)
	}
	for _, t := range result.Truncated {
		fmt.Fprintf(out, "%s: read the newest %d of %d refs; older ones were not reached\n",
			t.Query.Repo, t.Read, t.Matched)
	}
	for _, ref := range result.NewRefs {
		fmt.Fprintf(out, "%s %s %s appeared\n", ref.RepoID, ref.Kind, ref.Name)
	}
	reportSettled(out, result.Settled)
	reportEjected(out, result.Ejected)
	reportOverdue(out, result.Overdue)
	reportUnannounced(out, result.Unannounced)
	for _, a := range result.Woken.Actions {
		fmt.Fprintf(out, "%s is back: the deferral ran out\n", a.ID)
	}
	for _, p := range result.Woken.Projects {
		fmt.Fprintf(out, "%s is back: the deferral ran out\n", p.ID)
	}

	if !quiet {
		fmt.Fprintf(out, "polled %d, %d changed", result.Polled, result.ChangedCount())
		if len(result.Missing) > 0 {
			fmt.Fprintf(out, ", %d unreadable", len(result.Missing))
		}
		if result.SettledCount() > 0 {
			fmt.Fprintf(out, ", %d closed", result.SettledCount())
		}
		if result.EjectedCount() > 0 {
			fmt.Fprintf(out, ", %d ejected", result.EjectedCount())
		}
		if result.OverdueCount() > 0 {
			fmt.Fprintf(out, ", %d overdue", result.OverdueCount())
		}
		if n := len(result.Unannounced); n > 0 {
			fmt.Fprintf(out, ", %d unannounced", n)
		}
		if result.IssuesPolled == 1 {
			fmt.Fprint(out, ", 1 issue polled")
		} else if result.IssuesPolled > 1 {
			fmt.Fprintf(out, ", %d issues polled", result.IssuesPolled)
		}
		if result.RefsPolled > 0 {
			fmt.Fprintf(out, ", %d ref queries", result.RefsPolled)
		}
		// Rare by design — only when a step is waiting for a group and the
		// stored membership has aged out — so it is worth a mention when it
		// happens rather than a permanent column of zero.
		if result.Teams > 0 {
			fmt.Fprintf(out, ", %d team memberships read", result.Teams)
		}
		fmt.Fprintln(out)
	}
	return nil
}

// reportEjected prints what fell out of a merge queue, and what was put in the
// queue about it.
//
// Worth a line of its own because the pull request looks fine: approved,
// clean, every check green, and going nowhere. Nothing else on the page or in
// the log would say so.
func reportEjected(out io.Writer, all []store.Ejected) {
	for _, ejected := range all {
		fmt.Fprintf(out, "%s left the merge queue without merging\n", ejected.PR)
		if ejected.Action != nil {
			fmt.Fprintf(out, "  %s added to the queue\n", ejected.Action.ID)
		}
	}
}

// reportSettled prints what closed itself, and what that freed.
//
// It prints even under --quiet, because an action closing without anyone
// asking is the most surprising thing this system does, and finding out from
// a later `action list` is worse.
//
// Shared with `pr announce`, which settles for the same reason sync does: one
// report rather than two that could describe the same event differently.
func reportSettled(out io.Writer, all []store.Settled) {
	for _, settled := range all {
		// A ref wait has no pull request to name, so the verb carries the
		// line on its own rather than printing an empty subject.
		if settled.PR == "" {
			fmt.Fprintf(out, "%s closed: %s\n", settled.Action.ID, settled.Action.Verb)
		} else {
			fmt.Fprintf(out, "%s closed: %s is %s\n",
				settled.Action.ID, settled.PR, settled.Action.Verb)
		}
		for _, freed := range settled.Result.Unblocked {
			fmt.Fprintf(out, "  %s is now %s\n", freed.ID, freed.State)
		}
		for _, back := range settled.Result.Unhidden {
			fmt.Fprintf(out, "  %s is no longer hidden\n", back.ID)
		}
		for _, done := range settled.Result.StoodDown {
			fmt.Fprintf(out, "  %s stood down: %s\n", done.ID, done.Title)
		}
	}
}

// sortedKeys makes output deterministic where it is built from a map: a sync
// report keyed by pull request, a diagram keyed by parent project.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// reportOverdue prints the waits that have gone on too long.
//
// Like reportSettled it prints even under --quiet. The whole reason this
// exists is that a wait going bad was silent, and a quiet flag that silenced
// it again would defeat the point.
func reportOverdue(out io.Writer, overdue []store.Overdue) {
	for _, o := range overdue {
		fmt.Fprintf(out, "%s has been waiting %d days: %s\n",
			o.Action.ID, o.Waiting, o.Action.Title)
		// Worth saying: the point of raising one is that nobody has to
		// remember the line above.
		if o.Raised != nil {
			fmt.Fprintf(out, "  %s added to the queue\n", o.Raised.ID)
		}
	}
}

// reportUnannounced says what is waiting for a review nobody asked for, and
// says what to do about it: the whole point is that this is a different
// problem from a slow review, wanting a different response.
func reportUnannounced(out io.Writer, waits []store.Unannounced) {
	for _, w := range waits {
		fmt.Fprintf(out, "%s is waiting for a review of %s that nobody was asked for\n",
			w.Action.ID, w.PR)
		fmt.Fprintf(out, "  announce it, then `roz pr announce %s`\n", w.PR)
	}
}

// ownersText renders who a pull request needs, saying so when the answer is
// nobody rather than printing an empty list.
func ownersText(owners []string) string {
	if len(owners) == 0 {
		return "nobody: no CODEOWNERS rule covers what it touches"
	}
	return strings.Join(owners, ", ")
}
