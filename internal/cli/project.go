package cli

import "github.com/spf13/cobra"

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Bodies of work, allocated an SL identifier",
	}
	cmd.AddCommand(
		newProjectAddCmd(),
		newProjectSupersedeCmd(),
		newProjectListCmd(),
	)
	return cmd
}

func newProjectAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Allocate a new project and print its id",
		Args:  cobra.NoArgs,
		Run:   stub,
	}
	f := cmd.Flags()
	f.String("title", "", "what the project is (required)")
	f.String("summary", "", "how it relates to other items; not a design note")
	f.String("json", "", "remaining authored fields as a JSON object")
	_ = cmd.MarkFlagRequired("title")
	return cmd
}

func newProjectSupersedeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "supersede",
		Short: "Record that one project is the same work as another",
		Long: "The superseded project keeps its identifier and stops rendering; the\n" +
			"edge survives so old references still resolve.",
		Args: cobra.NoArgs,
		Run:  stub,
	}
	f := cmd.Flags()
	f.String("from", "", "project being superseded, e.g. SL32 (required)")
	f.String("into", "", "project that replaces it, e.g. SL94 (required)")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("into")
	return cmd
}

// newProjectListCmd is not in the sketch's verb list; the projects table is a
// page element, so the query has to exist somewhere.
func newProjectListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "The projects table",
		Args:  cobra.NoArgs,
		Run:   stub,
	}
	f := cmd.Flags()
	f.Bool("orphaned", false, "no open action and no snooze — how live work goes quiet")
	f.Bool("expired", false, "snoozed with a date that has passed")
	f.String("status", "", "filter to one status")
	return cmd
}
