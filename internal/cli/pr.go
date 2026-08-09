package cli

import "github.com/spf13/cobra"

func newPRCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Tracked pull requests, keyed repo#number",
	}
	cmd.AddCommand(newPRListCmd())
	return cmd
}

func newPRListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Tracked pull requests",
		Args:  cobra.NoArgs,
		Run:   stub,
	}
	f := cmd.Flags()
	f.Bool("stacked", false, "base_ref is not the default branch")
	f.Bool("frozen", false, "announced or commented on, so amend rather than force-push")
	return cmd
}
