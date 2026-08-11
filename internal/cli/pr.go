package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

func newPRCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Tracked pull requests, keyed repo#number",
	}
	cmd.AddCommand(
		newPRTrackCmd(),
		newPRSetCmd(),
		newPRAnnounceCmd(),
		newPRShowCmd(),
		newPRListCmd(),
	)
	return cmd
}

func newPRTrackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "track <repo#number>",
		Short: "Start tracking a pull request",
		Long: "Records the decision to track it, and nothing else. Every other column\n" +
			"is observed and belongs to sync, so a freshly tracked pull request is\n" +
			"empty until it is synced.\n\n" +
			"There is no column for the decision itself — the row's existence is it.\n\n" +
			"--pipeline is the exception: it says this pull request reaches merge\n" +
			"differently from the rest of its repository — a hotfix that skips\n" +
			"review, or protected code that needs more than the usual steps.\n" +
			"Leaving it unset is the ordinary case and means the repository's,\n" +
			"read when the chain is instantiated rather than copied now.",
		Args: cobra.ExactArgs(1),
		RunE: runPRTrack,
	}
	addPRPipelineFlag(cmd)
	addActorFlag(cmd)
	return cmd
}

func addPRPipelineFlag(cmd *cobra.Command) {
	cmd.Flags().String(flagPipeline, "",
		"how this one reaches merge, when it differs from its repository; "+
			"see `todo pipeline list`")
}

func runPRTrack(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	repo, number, err := store.ParsePRKey(args[0])
	if err != nil {
		return err
	}
	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// The repository has to be tracked first: its pipeline decides which
	// actions a pull request against it will want, so tracking one without
	// having said anything about the repository would start from a guess.
	switch _, err := tx.LoadGitHubRepo(ctx, repo); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%s is not tracked; run `todo repo track %s` first", repo, repo)
	case err != nil:
		return err
	}

	p := store.NewPR(repo, number)
	if err := applyPRPipelineFlag(cmd, p); err != nil {
		return err
	}

	// Check first rather than interpreting a constraint failure. The unique
	// index is still the backstop; this is only so the message says what
	// happened.
	switch _, err := tx.LoadPR(ctx, p.ID); {
	case err == nil:
		return fmt.Errorf("%s is already tracked", p.ID)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}

	// The same check `todo repo track` makes, and for the same reason: an
	// unknown pipeline should be reported as itself rather than as a foreign
	// key constraint. Unlike a repository, no default is filled in — unset
	// means the repository's.
	if err := checkPipelineUsable(ctx, tx, p.Pipeline); err != nil {
		return err
	}
	if err := tx.Insert(ctx, p); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout(), p.ID)
	return nil
}

// newPRSetCmd exists because --pipeline at track time alone would be a
// decision that cannot be revised. Whether a pull request is a hotfix is
// often learned after it is tracked, and there has to be a way back to the
// repository's chain.
//
// One flag is the whole command: everything else about a pull request is
// observed, and sync's to write.
func newPRSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <repo#number>",
		Short: "Change how one pull request reaches merge",
		Long: "The only authored column a pull request has. Everything else is\n" +
			"observed and belongs to sync.\n\n" +
			"An empty value clears it: --pipeline \"\" returns this pull request to\n" +
			"its repository's chain.\n\n" +
			"Changing it affects the chain the next close instantiates. Actions\n" +
			"already created are not revisited — they exist, and something may\n" +
			"already be waiting on them.",
		Args: cobra.ExactArgs(1),
		RunE: runPRSet,
	}
	addPRPipelineFlag(cmd)
	addActorFlag(cmd)
	return cmd
}

func runPRSet(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	if !cmd.Flags().Changed(flagPipeline) {
		return fmt.Errorf("nothing to set: pass --%s", flagPipeline)
	}
	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	before, err := tx.LoadPR(ctx, args[0])
	if err != nil {
		return notFoundOr(err, args[0])
	}
	after := before.Clone()
	if err := applyPRPipelineFlag(cmd, after); err != nil {
		return err
	}
	if err := checkPipelineUsable(ctx, tx, after.Pipeline); err != nil {
		return err
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
		fmt.Fprintf(out, "%s unchanged\n", before.ID)
		return nil
	}
	for _, change := range changes {
		fmt.Fprintf(out, "%s %s\n", before.ID, change)
	}
	return nil
}

func applyPRPipelineFlag(cmd *cobra.Command, p *store.PR) error {
	if !cmd.Flags().Changed(flagPipeline) {
		return nil
	}
	v, err := cmd.Flags().GetString(flagPipeline)
	if err != nil {
		return err
	}
	p.Pipeline = nullString(v)
	return nil
}

func newPRShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <repo#number>",
		Short: "Print one pull request in full",
		Args:  cobra.ExactArgs(1),
		RunE:  runPRShow,
	}
	addOutputFlag(cmd)
	return cmd
}

func runPRShow(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	p, err := tx.LoadPR(ctx, args[0])
	if err != nil {
		return notFoundOr(err, args[0])
	}

	return showRecord(cmd, ctx, tx, p, format)
}

func newPRListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Tracked pull requests",
		Args:  cobra.NoArgs,
		RunE:  runPRList,
	}
	f := cmd.Flags()
	f.Bool("stacked", false, "based on another tracked pull request")
	f.Bool("frozen", false, "announced or commented on, so amend rather than force-push")
	f.String("state", "", "OPEN, MERGED or CLOSED")
	addOutputFlag(cmd)
	return cmd
}

func runPRList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	filter, err := prFilterFrom(cmd)
	if err != nil {
		return err
	}
	prs, err := st.ListPRs(ctx, filter)
	if err != nil {
		return err
	}

	if format == outputJSON {
		encoded, err := store.MarshalRecords(prs)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}
	return writePRTable(cmd.OutOrStdout(), prs)
}

func prFilterFrom(cmd *cobra.Command) (store.PRFilter, error) {
	f := cmd.Flags()

	stacked, err := f.GetBool("stacked")
	if err != nil {
		return store.PRFilter{}, err
	}
	frozen, err := f.GetBool("frozen")
	if err != nil {
		return store.PRFilter{}, err
	}
	state, err := f.GetString("state")
	if err != nil {
		return store.PRFilter{}, err
	}
	return store.PRFilter{Stacked: stacked, Frozen: frozen, State: state}, nil
}

func writePRTable(out io.Writer, prs []*store.PR) error {
	if len(prs) == 0 {
		fmt.Fprintln(out, "no tracked pull requests")
		return nil
	}

	// The pipeline column appears only when something is using it. An
	// override is worth seeing and its absence is not, and the ordinary case
	// is every row reading "-" in a table that is wide already. Anything
	// parsing this should read -o json, which always carries the column.
	var overridden bool
	for _, p := range prs {
		overridden = overridden || p.Pipeline.Valid
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	header := "ID\tSTATE\tDRAFT\tREVIEW\tMERGE\tCHECKS\tFROZEN"
	if overridden {
		header += "\tPIPELINE"
	}
	fmt.Fprintln(w, header+"\tTITLE")

	for _, p := range prs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s",
			p.ID, nullText(p.State), nullBoolText(p.IsDraft),
			nullText(p.ReviewDecision), nullText(p.MergeStateStatus),
			nullText(p.ChecksState), yesNo(p.Frozen))
		if overridden {
			fmt.Fprintf(w, "\t%s", nullText(p.Pipeline))
		}
		fmt.Fprintf(w, "\t%s\n", orDash(p.Title))
	}
	return w.Flush()
}

func nullBoolText(v sql.NullBool) string {
	if !v.Valid {
		return "-"
	}
	return yesNo(v.Bool)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
