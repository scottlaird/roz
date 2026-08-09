package cli

import "github.com/spf13/cobra"

// newSyncCmd is the only writer of observed fields. It never writes an
// authored one, and it is read-only against the external systems.
func newSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:       "sync <source>",
		Short:     "Refresh observed fields from an external system",
		ValidArgs: []string{"github", "jira", "slack"},
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		Run:       stub,
	}
	f := cmd.Flags()
	f.Bool("dry-run", false, "report what would change without writing")
	return cmd
}

func newVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify <subject>",
		Short: "Stamp last_verified_at; changes nothing else",
		Long: "Records that an item was checked against reality, as distinct from\n" +
			"when it was last edited.",
		Args: cobra.ExactArgs(1),
		Run:  stub,
	}
}
