package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

func newPipelineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pipeline",
		Short: "What closing a verb instantiates",
		Long: "A pipeline is the chain of actions that follows a piece of work:\n" +
			"undraft, announce, wait for review, merge. Repositories differ in\n" +
			"which one they use, so `roz repo set --pipeline` picks per repository\n" +
			"and a newly tracked one takes the first active pipeline listed here.",
	}
	cmd.AddCommand(newPipelineListCmd())
	return cmd
}

func newPipelineListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Print the pipelines and their steps",
		Args:  cobra.NoArgs,
		RunE:  runPipelineList,
	}
	cmd.Flags().Bool("all", false, "include retired pipelines")
	addOutputFlag(cmd)
	return cmd
}

func runPipelineList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	all, err := cmd.Flags().GetBool("all")
	if err != nil {
		return err
	}
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	pipelines, err := st.ListPipelines(ctx, !all)
	if err != nil {
		return err
	}

	if format == outputJSON {
		encoded, err := store.MarshalRecords(pipelines)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}
	return writePipelineTable(cmd.OutOrStdout(), pipelines)
}

func writePipelineTable(out io.Writer, pipelines []*store.Pipeline) error {
	if len(pipelines) == 0 {
		fmt.Fprintln(out, "no pipelines")
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tLABEL\tACTIVE\tSTEPS")
	for _, p := range pipelines {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			p.Name, p.Label, yesNo(p.Active), strings.Join(p.Steps, " → "))
	}
	return w.Flush()
}
