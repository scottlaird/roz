package cli

import "github.com/spf13/cobra"

func newNoteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "note <subject> <text>",
		Short: "Append a note event to a subject",
		Args:  cobra.ExactArgs(2),
		Run:   stub,
	}
}

func newExceptionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exception",
		Short: "Record an exception event for a monitor to surface",
		Long: "Severity is separate from kind so monitors can filter on\n" +
			"severity = 'exception' without knowing the full kind vocabulary.",
		Args: cobra.NoArgs,
		Run:  stub,
	}
	f := cmd.Flags()
	f.String("subject", "", "subject id, e.g. NA102 (required)")
	f.String("kind", "", "e.g. unexpected_review_state (required)")
	f.String("note", "", "free text")
	_ = cmd.MarkFlagRequired("subject")
	_ = cmd.MarkFlagRequired("kind")
	return cmd
}

func newRenderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "render",
		Short: "Regenerate the status page from the database",
		Long:  "The page is a view; the database is the truth.",
		Args:  cobra.NoArgs,
		Run:   stub,
	}
	f := cmd.Flags()
	f.String("template", "", "template path, e.g. next-actions.html.tmpl (required)")
	f.String("out", "-", "output path, or - for stdout")
	_ = cmd.MarkFlagRequired("template")
	return cmd
}
