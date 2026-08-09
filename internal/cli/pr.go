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
			"There is no column for the decision itself — the row's existence is it.",
		Args: cobra.ExactArgs(1),
		RunE: runPRTrack,
	}
	addActorFlag(cmd)
	return cmd
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

	// The repository has to be tracked first: its review policy decides which
	// actions a pull request against it will want, so tracking one without
	// having said anything about the repository would start from a guess.
	switch _, err := tx.LoadGitHubRepo(ctx, repo); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%s is not tracked; run `todo repo track %s` first", repo, repo)
	case err != nil:
		return err
	}

	p := store.NewPR(repo, number)

	// Check first rather than interpreting a constraint failure. The unique
	// index is still the backstop; this is only so the message says what
	// happened.
	switch _, err := tx.LoadPR(ctx, p.ID); {
	case err == nil:
		return fmt.Errorf("%s is already tracked", p.ID)
	case !errors.Is(err, sql.ErrNoRows):
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

	if format == outputJSON {
		encoded, err := store.MarshalRecord(p)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}
	return writeRecordDetail(cmd.OutOrStdout(), p)
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

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATE\tDRAFT\tREVIEW\tMERGE\tCHECKS\tFROZEN\tTITLE")
	for _, p := range prs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			p.ID, nullText(p.State), nullBoolText(p.IsDraft),
			nullText(p.ReviewDecision), nullText(p.MergeStateStatus),
			nullText(p.ChecksState), yesNo(p.Frozen), orDash(p.Title))
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
