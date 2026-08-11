// Package cli defines the todo command tree.
//
// The tree is the surface: every leaf is implemented, and the shape of the
// command line is settled here rather than emerging from the packages
// underneath it. `todo mcp` derives its tools from this same tree, so a flag
// added to a command is a tool argument without anything else being written.
package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

// dbPath is the SQLite file every command will operate on.
var dbPath string

// NewRootCmd builds the full command tree.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "todo",
		Short: "Track projects, actions and pull requests",
		Long: "todo keeps a work queue correct mechanically: projects (TD) are what you\n" +
			"plan from, actions (NA) are what you read. Every mutation is an event.",
		SilenceUsage: true,
	}

	// --db is the one setting that cannot live in the database, since it says
	// which database to open. Everything else is `todo config`.
	root.PersistentFlags().StringVar(&dbPath, "db", defaultDBPath(),
		"path to the SQLite database (env: TODO_DB)")

	root.AddCommand(
		newInitCmd(),
		newConfigCmd(),
		newProjectCmd(),
		newActionCmd(),
		newPRCmd(),
		newRepoCmd(),
		newCalendarCmd(),
		newSyncCmd(),
		newSyncerCmd(),
		newVerbCmd(),
		newJiraCmd(),
		newPipelineCmd(),
		newVerifyCmd(),
		newNoteCmd(),
		newExceptionCmd(),
		newRenderCmd(),
		newServeCmd(),
		newMCPCmd(),
		newWatchCmd(),
	)
	return root
}

// defaultDBPath is the --db default: TODO_DB when set, otherwise the
// user-level data location. A resolution failure yields an empty default
// rather than an error, so --help still works on a machine with no usable
// home directory; commands report the problem when they open the database.
func defaultDBPath() string {
	if p := os.Getenv("TODO_DB"); p != "" {
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
