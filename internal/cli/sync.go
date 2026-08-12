package cli

import (
	"fmt"
	"io"
	"sort"

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
			"Only pull requests already tracked with `roz pr track` are polled.",
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
	reportSettled(out, result.Settled)
	reportEjected(out, result.Ejected)
	reportOverdue(out, result.Overdue)

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
		fmt.Fprintf(out, "%s closed: %s is %s\n",
			settled.Action.ID, settled.PR, settled.Action.Verb)
		for _, freed := range settled.Result.Unblocked {
			fmt.Fprintf(out, "  %s is now %s\n", freed.ID, freed.State)
		}
		for _, back := range settled.Result.Unhidden {
			fmt.Fprintf(out, "  %s is no longer hidden\n", back.ID)
		}
	}
}

// sortedKeys makes the report deterministic, since both maps are keyed by
// pull request.
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
	}
}
