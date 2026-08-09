package cli

import "github.com/spf13/cobra"

// newInitCmd is not in the design sketch's verb list, but the database has to
// come from somewhere.
func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create the database and apply the schema",
		Long: "Applies internal/schema/schema.sql to a new database and seeds the\n" +
			"sequence and actionverb vocabulary tables.",
		Args: cobra.NoArgs,
		Run:  stub,
	}
	cmd.Flags().Bool("force", false, "recreate the schema even if the file exists")
	return cmd
}
