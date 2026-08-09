// Package cli defines the todo command tree.
//
// Every leaf command is currently a stub: it prints its path and exits 1.
// The tree itself is the point — the surface is settled here, and behaviour
// gets filled in underneath it.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// dbPath is the SQLite file every command will operate on.
var dbPath string

// stub is the Run function for every command that has no implementation yet.
func stub(cmd *cobra.Command, _ []string) {
	fmt.Fprintf(os.Stderr, "todo: %s is not implemented\n", cmd.CommandPath())
	os.Exit(1)
}

// NewRootCmd builds the full command tree.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "todo",
		Short: "Track projects, actions and pull requests",
		Long: "todo keeps a work queue correct mechanically: projects (SL) are what you\n" +
			"plan from, actions (NA) are what you read. Every mutation is an event.",
		SilenceUsage: true,
	}

	root.PersistentFlags().StringVar(&dbPath, "db", defaultDBPath(),
		"path to the SQLite database (env: TODO_DB)")

	root.AddCommand(
		newInitCmd(),
		newProjectCmd(),
		newActionCmd(),
		newPRCmd(),
		newSyncCmd(),
		newVerifyCmd(),
		newNoteCmd(),
		newExceptionCmd(),
		newRenderCmd(),
	)
	return root
}

func defaultDBPath() string {
	if p := os.Getenv("TODO_DB"); p != "" {
		return p
	}
	return "todo.db"
}

// Execute runs the root command.
func Execute() {
	if err := NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
