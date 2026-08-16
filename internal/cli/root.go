// Package cli defines the roz command tree.
//
// The tree is the surface: every leaf is implemented, and the shape of the
// command line is settled here rather than emerging from the packages
// underneath it. `roz mcp` derives its tools from this same tree, so a flag
// added to a command is a tool argument without anything else being written.
package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

// NewRootCmd builds the full command tree.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "roz",
		Short: "Track projects, actions and pull requests",
		Long: "roz keeps a work queue correct mechanically: projects (ROZ) are what you\n" +
			"plan from, actions (NA) are what you read. Every mutation is an event.",
		SilenceUsage: true,
	}

	// --db is the one setting that cannot live in the database, since it says
	// which database to open. Everything else is `roz config`.
	// Bound to the flag rather than to a package variable, so the value lives
	// on this command tree and not on the process. Two trees in one process
	// then cannot tread on each other — which `roz mcp` does, building a
	// fresh tree per tool call, and would do concurrently the moment it is
	// served over anything but stdio.
	root.PersistentFlags().String("db", defaultDBPath(),
		"path to the SQLite database (env: ROZ_DB)")

	root.AddCommand(
		newInitCmd(),
		newDBCmd(),
		newConfigCmd(),
		newProjectCmd(),
		newActionCmd(),
		newPRCmd(),
		newRepoCmd(),
		newCalendarCmd(),
		newSyncCmd(),
		newSyncerCmd(),
		newVerbCmd(),
		newIssueCmd(),
		newPipelineCmd(),
		newVerifyCmd(),
		newCodeownersCmd(),
		newOwnerCmd(),
		newViewCmd(),
		newNoteCmd(),
		newExceptionCmd(),
		newPageCmd(),
		newRefCmd(),
		newServeCmd(),
		newMCPCmd(),
		newWatchCmd(),
	)
	return root
}

// defaultDBPath is the --db default: ROZ_DB when set, otherwise the
// user-level data location. A resolution failure yields an empty default
// rather than an error, so --help still works on a machine with no usable
// home directory; commands report the problem when they open the database.
func defaultDBPath() string {
	if p := os.Getenv("ROZ_DB"); p != "" {
		return p
	}
	p, err := store.DefaultDBPath()
	if err != nil {
		return ""
	}
	return p
}

// Execute runs the root command.
func Execute() {
	if err := NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
