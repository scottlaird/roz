package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

// newInitCmd is not in the design sketch's verb list, but the database has to
// come from somewhere.
func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create the database and apply the schema",
		Long: "Creates the database and its parent directory, then applies the schema.\n" +
			"Safe to re-run: an existing database is left exactly as it is.",
		Args: cobra.NoArgs,
		RunE: runInit,
	}
}

func runInit(cmd *cobra.Command, _ []string) error {
	if dbPath == "" {
		return errors.New("no database path: pass --db or set TODO_DB")
	}

	created, err := store.Init(dbPath)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if created {
		fmt.Fprintf(out, "initialised %s\n", dbPath)
	} else {
		fmt.Fprintf(out, "%s is already initialised\n", dbPath)
	}
	return nil
}
