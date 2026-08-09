package cli

import "github.com/spf13/cobra"

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
