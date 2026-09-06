package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
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
		newPRChainCmd(),
		newPRShowCmd(),
		newPRListCmd(),
		newPRLinkIssueCmd(),
		newPRUnlinkIssueCmd(),
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
			"read when the chain is instantiated rather than copied now.\n\n" +
			"--because says why you are tracking it. Tracking one you did not\n" +
			"write is the case it exists for: a review has different actions and a\n" +
			"different reason to stop tracking it. There is no default, because\n" +
			"assuming you wrote it would be right most of the time and still be\n" +
			"the tool inventing a fact.",
		Args: cobra.ExactArgs(1),
		RunE: runPRTrack,
	}
	addPRPipelineFlag(cmd)
	addPRBecauseFlag(cmd)
	addActorFlag(cmd)
	return cmd
}

func addPRPipelineFlag(cmd *cobra.Command) {
	cmd.Flags().String(flagPipeline, "",
		"how this one reaches merge, when it differs from its repository; "+
			"see `roz pipeline list`")
}

const flagBecause = "because"

func addPRBecauseFlag(cmd *cobra.Command) {
	cmd.Flags().String(flagBecause, "",
		"why it is tracked: "+strings.Join(store.TrackingReasons, ", ")+
			"; unset leaves it unstated")
}

func applyPRBecauseFlag(cmd *cobra.Command, p *store.PR) error {
	if !cmd.Flags().Changed(flagBecause) {
		return nil
	}
	v, err := cmd.Flags().GetString(flagBecause)
	if err != nil {
		return err
	}
	if v == "" {
		p.TrackedBecause = sql.NullString{}
		return nil
	}
	if err := store.ValidateTrackingReason(v); err != nil {
		return err
	}
	p.TrackedBecause = sql.NullString{String: v, Valid: true}
	return nil
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

	// The repository has to be tracked first: its pipeline decides which
	// actions a pull request against it will want, so tracking one without
	// having said anything about the repository would start from a guess.
	switch _, err := tx.LoadGitHubRepo(ctx, repo); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%s is not tracked; run `roz repo track %s` first", repo, repo)
	case err != nil:
		return err
	}

	p := store.NewPR(repo, number)
	if err := applyPRPipelineFlag(cmd, p); err != nil {
		return err
	}
	if err := applyPRBecauseFlag(cmd, p); err != nil {
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

	// The same check `roz repo track` makes, and for the same reason: an
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
		Short: "Change how one pull request reaches merge, or why it is tracked",
		Long: "The two authored columns a pull request has. Everything else is\n" +
			"observed and belongs to sync.\n\n" +
			"An empty value clears either: --pipeline \"\" returns this pull request\n" +
			"to its repository's chain, and --because \"\" leaves the reason\n" +
			"unstated again.\n\n" +
			"Changing it affects the chain the next close instantiates. Actions\n" +
			"already created are not revisited — they exist, and something may\n" +
			"already be waiting on them.",
		Args: cobra.ExactArgs(1),
		RunE: runPRSet,
	}
	addPRPipelineFlag(cmd)
	addPRBecauseFlag(cmd)
	addActorFlag(cmd)
	return cmd
}

func runPRSet(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	if !cmd.Flags().Changed(flagPipeline) && !cmd.Flags().Changed(flagBecause) {
		return fmt.Errorf("nothing to set: pass --%s or --%s", flagPipeline, flagBecause)
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

	before, err := tx.LoadPR(ctx, args[0])
	if err != nil {
		return notFoundOr(err, args[0])
	}
	after := before.Clone()
	if err := applyPRPipelineFlag(cmd, after); err != nil {
		return err
	}
	if err := applyPRBecauseFlag(cmd, after); err != nil {
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
	st, err := openStore(cmd)
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

	// The pipeline column is the override, so it reads "-" both for a pull
	// request following its repository's chain and for one following nothing
	// at all. Those are opposite situations, and reading the first as the
	// second is how a pull request gets left with no open actions and nobody
	// notices for three days — scottlaird/roz#96. chain answers it directly:
	// which pipeline applies, where it came from, and what nothing covers.
	chain, err := tx.ChainOf(ctx, p.ID)
	if err != nil {
		return err
	}

	return showRecordWith(cmd, ctx, tx, p, format, map[string]string{"chain": chain.Summary()})
}

func newPRListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Tracked pull requests",
		Long: "--since is the week in review: what merged, oldest first, so the\n" +
			"list reads in the order the week happened.\n\n" +
			"  roz pr list --since 2026-08-06\n\n" +
			"It reads merged_at, so it selects merged pull requests — \"since\" is\n" +
			"about when something finished, and an open one has not. --state\n" +
			"MERGED alongside it is redundant rather than wrong, and on its own\n" +
			"gives the same order over every merge ever tracked.\n\n" +
			"merged_at is GitHub's own timestamp rather than when roz noticed.\n" +
			"The two differ for a pull request tracked after it merged, which\n" +
			"has no transition in the log at all.",
		Args: cobra.NoArgs,
		RunE: runPRList,
	}
	f := cmd.Flags()
	f.Bool("stacked", false, "based on another tracked pull request")
	f.Bool("frozen", false, "announced or commented on, so amend rather than force-push")
	f.String("state", "", "OPEN, MERGED or CLOSED")
	f.String(flagBecause, "",
		"why it is tracked: "+strings.Join(store.TrackingReasons, ", ")+
			" — most usefully what you are on the hook to review")
	f.String(flagSince, "", "merged on or after this date or timestamp; selects merged ones")
	addSortFlag(cmd, prColumns)
	addListingFlags(cmd, prColumns)
	return cmd
}

// prColumns is what `pr list` can show.
//
// Three columns appear only when something is using them, which is the rule
// the hand-written table applied by hand: an exception is worth seeing and its
// absence is not, and the ordinary case was every row reading "-" in a table
// that is wide already.
var prColumns = columnSet[*store.PR]{
	blank: &store.PR{},
	declared: []column[*store.PR]{
		{name: "is_draft", header: "DRAFT"},
		{name: "review_decision", header: "REVIEW"},
		{name: "merge_state_status", header: "MERGE"},
		{name: "checks_state", header: "CHECKS"},
		{
			name: "merged_at", header: "MERGED",
			render: func(p *store.PR, _ renderContext) string { return dateCell(p.MergedAt) },
			showIf: func(rows []*store.PR, _ renderContext) bool { return anyPR(rows, prIsMerged) },
		},
		{
			name:   "tracked_because",
			header: "BECAUSE",
			showIf: func(rows []*store.PR, _ renderContext) bool { return anyPR(rows, prIsExplained) },
		},
		{
			name:   "pipeline",
			showIf: func(rows []*store.PR, _ renderContext) bool { return anyPR(rows, prIsOverridden) },
		},
	},
	defaults: []string{
		"id", "state", "is_draft", "review_decision", "merge_state_status",
		"checks_state", "frozen", "merged_at", "tracked_because", "pipeline", "title",
	},
	empty: "no tracked pull requests",
}

func anyPR(rows []*store.PR, has func(*store.PR) bool) bool {
	for _, p := range rows {
		if has(p) {
			return true
		}
	}
	return false
}

func prIsMerged(p *store.PR) bool     { return p.MergedAt.Valid }
func prIsExplained(p *store.PR) bool  { return p.TrackedBecause.Valid }
func prIsOverridden(p *store.PR) bool { return p.Pipeline.Valid }

func runPRList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	query, err := prFilterFrom(cmd)
	if err != nil {
		return err
	}
	// The one listing that pushes a CEL filter into its query. Compiled here
	// so the WHERE fragment reaches the SELECT; whatever SQLite could not take
	// is compiled again by runListing and run over the rows, which agrees
	// because the split is a function of the expression alone.
	cel, err := filterFrom(cmd, &store.PR{})
	if err != nil {
		return err
	}
	query.Where, query.WhereArgs = cel.Take()
	// Whatever the query could not take runs in Go, and a traversal there
	// needs somewhere to read the far side from.
	cel.WithLoader(ctx, storeLoader{st: st})

	prs, err := st.ListPRs(ctx, query)
	if err != nil {
		return err
	}

	return runListing(cmd, prColumns, prs, renderContext{})
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
	because, err := f.GetString(flagBecause)
	if err != nil {
		return store.PRFilter{}, err
	}
	if because != "" {
		if err := store.ValidateTrackingReason(because); err != nil {
			return store.PRFilter{}, err
		}
	}
	since, err := f.GetString(flagSince)
	if err != nil {
		return store.PRFilter{}, err
	}
	if since != "" {
		// A vague date would compare as text and quietly match nothing, which
		// on a listing looks like an answer.
		if since, err = validateTimestamp(flagSince, since); err != nil {
			return store.PRFilter{}, err
		}
	}
	_, sort, err := sortFrom(cmd, prColumns)
	if err != nil {
		return store.PRFilter{}, err
	}
	return store.PRFilter{
		Stacked: stacked, Frozen: frozen, State: state,
		Because: because, Since: since, Sort: sort,
	}, nil
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

// newPRChainCmd repairs the case a pipeline cannot reach on its own.
func newPRChainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chain <repo#number>",
		Short: "Create the pipeline steps this pull request is missing",
		Long: "A chain is only ever extended by closing a step that is already part\n" +
			"of one. So an action made with `action add` — even one carrying a\n" +
			"pipeline verb, closing on the same predicate — starts nothing when it\n" +
			"closes, and the pull request drops out of the queue at the moment it\n" +
			"stops being your problem and becomes a thing to watch.\n\n" +
			"This is the repair, and it is explicit rather than automatic: a single\n" +
			"action added against a pull request you are only lightly tracking\n" +
			"should not quietly acquire three more.\n\n" +
			"It creates only what is missing. Steps already done are not written\n" +
			"back — the log would gain closes nobody performed, and the numbering\n" +
			"would run out of order — so a chain reconstructed after the fact is\n" +
			"appended rather than interleaved. Running it twice creates nothing the\n" +
			"second time.\n\n" +
			"Actions that already exist are left exactly as they are, including\n" +
			"their blockers. New steps hang off the last open one ahead of them.\n\n" +
			"`roz pr show` names the pipeline that applies and the steps nothing\n" +
			"covers, which is how to see what this would do before doing it.",
		Args: cobra.ExactArgs(1),
		RunE: runPRChain,
	}
	addActorFlag(cmd)
	return cmd
}

func runPRChain(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	if _, _, err := store.ParsePRKey(args[0]); err != nil {
		return err
	}
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	result, err := st.InstantiateChain(ctx, actor, args[0])
	if err != nil {
		return notFoundOr(err, args[0])
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s %s\n", args[0], result.State.Summary())
	if len(result.Created) == 0 {
		fmt.Fprintln(out, "  nothing missing")
		return nil
	}
	for i, a := range result.Created {
		blocked := ""
		if by := result.BlockedBy[i]; by != "" {
			blocked = fmt.Sprintf(" (blocked by %s)", by)
		}
		fmt.Fprintf(out, "  created %s %s%s\n", a.ID, a.Title, blocked)
	}
	return nil
}
