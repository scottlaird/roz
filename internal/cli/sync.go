package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/ghsync"
	"github.com/scottlaird/todo/internal/github"
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
			"Only pull requests already tracked with `todo pr track` are polled.",
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
	st, err := openStore()
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

	if !quiet {
		fmt.Fprintf(out, "polled %d, %d changed", result.Polled, result.ChangedCount())
		if len(result.Missing) > 0 {
			fmt.Fprintf(out, ", %d unreadable", len(result.Missing))
		}
		fmt.Fprintln(out)
	}
	return nil
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
