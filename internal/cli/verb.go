package cli

import (
	"database/sql"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

func newVerbCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verb",
		Short: "The action vocabulary",
		Long: "Each verb says how an action using it closes. A predicate verb names a\n" +
			"function in the code; a human verb closes only when someone says so, and\n" +
			"those are the only ones that reach the queue as thinking work.",
	}
	cmd.AddCommand(newVerbListCmd(), newVerbSetCmd())
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
	// WAIT is here because it is the one column that is meant to be tuned,
	// and a setting you cannot read is a setting you cannot change with any
	// confidence.
	fmt.Fprintln(w, "VERB\tCLOSES\tPREDICATE\tRANK\tPR\tWAIT\tACTIVE\tDESCRIPTION")
	for _, v := range verbs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			v.Verb, v.Closes, nullText(v.PredicateKey), v.RankClass,
			yesNo(v.RequiresPR), nullIntText(v.WaitDays),
			yesNo(v.Active), v.Description)
	}
	return w.Flush()
}

const (
	flagWaitDays  = "wait-days"
	flagRankClass = "rank-class"
)

func newVerbSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <verb>",
		Short: "Change a verb's settings",
		Long: "Settings, not definitions. How long a wait is reasonable and which rank\n" +
			"class a verb sorts in are judgements about one person's queue, and\n" +
			"tuning a number that is meant to be tuned should not mean a migration\n" +
			"and a rebuild.\n\n" +
			"What a verb *means* is not editable here. `closes` and the predicate it\n" +
			"names are checked against the build when the database opens, so a verb\n" +
			"naming a predicate this binary lacks is refused at startup. Letting\n" +
			"those be edited would turn that check into a failure at closing time,\n" +
			"which is the worst moment to discover it.\n\n" +
			"Verb rows are authored, so a change lands in the log like any other.",
		Args: cobra.ExactArgs(1),
		RunE: runVerbSet,
	}
	f := cmd.Flags()
	f.String(flagWaitDays, "",
		"calendar days of waiting that are reasonable before it is worth noticing; empty means never")
	f.String(flagRankClass, "", "click, decide, session or wait")
	addActorFlag(cmd)
	return cmd
}

func runVerbSet(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	f := cmd.Flags()

	if !f.Changed(flagWaitDays) && !f.Changed(flagRankClass) {
		return fmt.Errorf("nothing to set: pass --%s or --%s", flagWaitDays, flagRankClass)
	}

	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	before, err := tx.LoadVerb(ctx, args[0])
	if err != nil {
		return notFoundOr(err, args[0])
	}
	after := before.Clone()

	if f.Changed(flagWaitDays) {
		raw, err := f.GetString(flagWaitDays)
		if err != nil {
			return err
		}
		if after.WaitDays, err = waitDaysFrom(raw); err != nil {
			return err
		}
	}
	if f.Changed(flagRankClass) {
		raw, err := f.GetString(flagRankClass)
		if err != nil {
			return err
		}
		if err := store.ValidateRankClass(raw); err != nil {
			return err
		}
		after.RankClass = raw
	}

	changes, err := tx.Update(ctx, before, after)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if len(changes) == 0 {
		fmt.Fprintf(out, "%s unchanged\n", before.Verb)
		return nil
	}
	for _, change := range changes {
		fmt.Fprintf(out, "%s %s\n", before.Verb, change)
	}
	return nil
}

// waitDaysFrom reads --wait-days. Empty clears it, which is how a verb is told
// it never times out: work of your own cannot be overdue, only undone.
func waitDaysFrom(raw string) (sql.NullInt64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return sql.NullInt64{}, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 0 {
		return sql.NullInt64{}, fmt.Errorf(
			"--%s %q is not a whole number of days; pass a number, or nothing to never time out",
			flagWaitDays, raw)
	}
	return sql.NullInt64{Int64: int64(days), Valid: true}, nil
}
