package cli

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

func newVerbCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verb",
		Short: "The action vocabulary",
		Long: "Each verb says how an action using it closes. A predicate verb names a\n" +
			"function in the code; a human verb closes only when someone says so, and\n" +
			"those are the only ones that reach the queue as thinking work.",
	}
	cmd.AddCommand(newVerbListCmd())
	return cmd
}

func newVerbListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Print the verb vocabulary",
		Args:  cobra.NoArgs,
		RunE:  runVerbList,
	}
	cmd.Flags().Bool("all", false, "include retired verbs")
	addOutputFlag(cmd)
	return cmd
}

func runVerbList(cmd *cobra.Command, _ []string) error {
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

	verbs, err := st.ListVerbs(ctx, !all)
	if err != nil {
		return err
	}

	if format == outputJSON {
		encoded, err := store.MarshalRecords(verbs)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}
	return writeVerbTable(cmd.OutOrStdout(), verbs)
}

func writeVerbTable(out io.Writer, verbs []*store.ActionVerb) error {
	if len(verbs) == 0 {
		fmt.Fprintln(out, "no verbs")
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "VERB\tCLOSES\tPREDICATE\tRANK\tPR\tACTIVE\tDESCRIPTION")
	for _, v := range verbs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			v.Verb, v.Closes, nullText(v.PredicateKey), v.RankClass,
			yesNo(v.RequiresPR), yesNo(v.Active), v.Description)
	}
	return w.Flush()
}
