package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

const (
	flagVerb    = "verb"
	flagProject = "project"
	flagWhy     = "why"
	flagRankPin = "rank-pin"
)

func newActionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "action",
		Short: "One thing to do next, allocated an NA identifier",
		Long: "An action's verb decides how it closes. A predicate verb closes when\n" +
			"GitHub says the work is done; a human verb closes when you say so, and\n" +
			"those are the only ones that reach the queue as thinking work.\n\n" +
			"`todo verb list` shows the vocabulary.",
	}
	cmd.AddCommand(
		newActionAddCmd(),
		newActionShowCmd(),
		newActionSetCmd(),
		newActionSnoozeCmd(),
		newActionWakeCmd(),
		newActionListCmd(),
		newActionAddBlockerCmd(),
		newActionHideBehindCmd(),
		newActionLinkPRCmd(),
		newActionCloseCmd(),
	)
	return cmd
}

// actionFieldFlags are the authored columns settable from the command line.
// closed_at and closed_reason are absent: `action close` sets those, with the
// cascade that belongs to them.
var actionFieldFlags = []string{
	flagTitle, flagVerb, flagProject, flagWhy, flagStatus, flagRankPin,
	flagSnoozeUntil, flagSnoozeReason,
}

func addActionFieldFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String(flagTitle, "", "what to do")
	f.String(flagVerb, "", "from the vocabulary; see `todo verb list`")
	f.String(flagProject, "", "project this advances, e.g. SL200; omit if it advances nothing")
	f.String(flagWhy, "", "what it unblocks; one sentence maximum")
	f.String(flagStatus, "", strings.Join(store.ActionStates, ", "))
	f.Int(flagRankPin, 0, "manual sort override; 0 clears it")
	f.String(flagSnoozeUntil, "", "ISO-8601 date or timestamp; requires state snoozed")
	f.String(flagSnoozeReason, "", "why it is deferred")
}

func newActionAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Allocate a new action and print its id",
		Args:  cobra.NoArgs,
		RunE:  runActionAdd,
	}
	addActionFieldFlags(cmd)
	_ = cmd.MarkFlagRequired(flagTitle)
	_ = cmd.MarkFlagRequired(flagVerb)
	addActorFlag(cmd)
	return cmd
}

func runActionAdd(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	f := cmd.Flags()

	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	title, err := f.GetString(flagTitle)
	if err != nil {
		return err
	}
	verb, err := f.GetString(flagVerb)
	if err != nil {
		return err
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	a := store.NewAction(title, verb)
	if err := applyActionFlags(cmd, a); err != nil {
		return err
	}
	if err := checkActionConsistency(a); err != nil {
		return err
	}

	// Validate in a transaction of its own, and let it go before allocating.
	//
	// Allocation writes on its own connection, by design — a failed insert
	// must still consume the number. A read transaction left open across it
	// cannot then upgrade to a writer: its snapshot is stale, and SQLite
	// returns SQLITE_BUSY_SNAPSHOT rather than waiting.
	if err := checkActionReferences(ctx, st, actor, a); err != nil {
		return err
	}

	// Allocate after validating: rejected input should not consume a number.
	if err := st.AllocateAction(ctx, a); err != nil {
		return err
	}

	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := tx.Insert(ctx, a); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout(), a.ID)
	return nil
}

// checkActionReferences confirms the verb and project exist, in a
// transaction that is finished with before anything else runs.
func checkActionReferences(ctx context.Context, st *store.Store, actor store.Actor, a *store.Action) error {
	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := checkVerbUsable(ctx, tx, a.Verb); err != nil {
		return err
	}
	return checkProjectExists(ctx, tx, a.ProjectID)
}

// checkVerbUsable reports an unknown or retired verb as itself, rather than
// letting the foreign key report a constraint.
func checkVerbUsable(ctx context.Context, tx *store.Tx, verb string) error {
	v, err := tx.LoadVerb(ctx, verb)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%q is not a verb; see `todo verb list`", verb)
	}
	if err != nil {
		return err
	}
	if !v.Active {
		return fmt.Errorf("%q is retired and cannot be used for new actions", verb)
	}
	return nil
}

func checkProjectExists(ctx context.Context, tx *store.Tx, project sql.NullString) error {
	if !project.Valid {
		return nil
	}
	if _, err := tx.LoadProject(ctx, project.String); err != nil {
		return notFoundOr(err, project.String)
	}
	return nil
}

func applyActionFlags(cmd *cobra.Command, a *store.Action) error {
	f := cmd.Flags()

	if f.Changed(flagTitle) {
		v, err := f.GetString(flagTitle)
		if err != nil {
			return err
		}
		a.Title = v
	}
	if f.Changed(flagVerb) {
		v, err := f.GetString(flagVerb)
		if err != nil {
			return err
		}
		a.Verb = v
	}
	if f.Changed(flagProject) {
		v, err := f.GetString(flagProject)
		if err != nil {
			return err
		}
		a.ProjectID = nullString(v)
	}
	if f.Changed(flagWhy) {
		v, err := f.GetString(flagWhy)
		if err != nil {
			return err
		}
		a.Why = v
	}
	if f.Changed(flagStatus) {
		v, err := f.GetString(flagStatus)
		if err != nil {
			return err
		}
		a.State = v
	}
	if f.Changed(flagRankPin) {
		v, err := f.GetInt(flagRankPin)
		if err != nil {
			return err
		}
		if v == 0 {
			a.RankPin = sql.NullInt64{}
		} else {
			a.RankPin = sql.NullInt64{Int64: int64(v), Valid: true}
		}
	}
	if f.Changed(flagSnoozeUntil) {
		v, err := f.GetString(flagSnoozeUntil)
		if err != nil {
			return err
		}
		if v != "" {
			if v, err = validateTimestamp(flagSnoozeUntil, v); err != nil {
				return err
			}
		}
		a.SnoozeUntil = nullString(v)
	}
	if f.Changed(flagSnoozeReason) {
		v, err := f.GetString(flagSnoozeReason)
		if err != nil {
			return err
		}
		a.SnoozeReason = v
	}
	return nil
}

// checkActionConsistency reports the schema's couplings before they become
// CHECK violations, naming the verb that moves both halves.
func checkActionConsistency(a *store.Action) error {
	snoozed := a.State == store.ActionSnoozed
	dated := a.SnoozeUntil.Valid

	switch {
	case snoozed && !dated:
		return fmt.Errorf("state %s needs a date: use `todo action snooze %s --%s <date>`",
			store.ActionSnoozed, a.ID, flagSnoozeUntil)
	case dated && !snoozed:
		return fmt.Errorf("a snooze date needs state %s: use `todo action snooze %s --%s <date>`",
			store.ActionSnoozed, a.ID, flagSnoozeUntil)
	}

	// closed_at and state move together; `action close` is what sets them.
	closed := a.ClosedAt.Valid
	finished := a.State == store.ActionDone || a.State == store.ActionDropped
	if closed != finished {
		return fmt.Errorf("state %s cannot be set here: use `todo action close %s`", a.State, a.ID)
	}
	return nil
}

func newActionSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <action>",
		Short: "Change authored columns on an existing action",
		Long: "Only the columns given are touched. Closing is not one of them —\n" +
			"`todo action close` does that, with the cascade that belongs to it.",
		Args: cobra.ExactArgs(1),
		RunE: runActionSet,
	}
	addActionFieldFlags(cmd)
	addActorFlag(cmd)
	return cmd
}

func runActionSet(cmd *cobra.Command, args []string) error {
	f := cmd.Flags()

	var given bool
	for _, flag := range actionFieldFlags {
		given = given || f.Changed(flag)
	}
	if !given {
		return fmt.Errorf("nothing to set: pass one of --%s", strings.Join(actionFieldFlags, ", --"))
	}

	return updateAction(cmd, args[0], func(ctx context.Context, tx *store.Tx, a *store.Action) error {
		if err := applyActionFlags(cmd, a); err != nil {
			return err
		}
		if f.Changed(flagVerb) {
			if err := checkVerbUsable(ctx, tx, a.Verb); err != nil {
				return err
			}
		}
		if f.Changed(flagProject) {
			if err := checkProjectExists(ctx, tx, a.ProjectID); err != nil {
				return err
			}
		}
		return checkActionConsistency(a)
	})
}

func newActionSnoozeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snooze <action>",
		Short: "Defer an action to a real date",
		Long: "--snooze-until must be a date or timestamp. \"Next week\" is not one,\n" +
			"and that is the point: a snooze nobody can act on is how work goes\n" +
			"quiet. `todo action list --expired` is what finds them again.",
		Args: cobra.ExactArgs(1),
		RunE: runActionSnooze,
	}
	f := cmd.Flags()
	f.String(flagSnoozeUntil, "", "ISO-8601 date or timestamp (required)")
	f.String(flagSnoozeReason, "", "why it is deferred")
	_ = cmd.MarkFlagRequired(flagSnoozeUntil)
	addActorFlag(cmd)
	return cmd
}

func runActionSnooze(cmd *cobra.Command, args []string) error {
	f := cmd.Flags()

	until, err := f.GetString(flagSnoozeUntil)
	if err != nil {
		return err
	}
	if until, err = validateTimestamp(flagSnoozeUntil, until); err != nil {
		return err
	}
	reason, err := f.GetString(flagSnoozeReason)
	if err != nil {
		return err
	}

	return updateAction(cmd, args[0], func(_ context.Context, _ *store.Tx, a *store.Action) error {
		if !a.IsOpen() {
			return fmt.Errorf("%s is %s; there is nothing to defer", a.ID, a.State)
		}
		a.State = store.ActionSnoozed
		a.SnoozeUntil = sql.NullString{String: until, Valid: true}
		a.SnoozeReason = reason
		return nil
	})
}

func newActionWakeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wake <action>",
		Short: "Clear a snooze",
		Args:  cobra.ExactArgs(1),
		RunE:  runActionWake,
	}
	cmd.Flags().String(flagStatus, store.ActionReady, "state to wake into")
	addActorFlag(cmd)
	return cmd
}

func runActionWake(cmd *cobra.Command, args []string) error {
	state, err := cmd.Flags().GetString(flagStatus)
	if err != nil {
		return err
	}
	switch state {
	case store.ActionReady, store.ActionBlocked:
	default:
		return fmt.Errorf("--%s %s does not wake anything: use %s or %s",
			flagStatus, state, store.ActionReady, store.ActionBlocked)
	}

	return updateAction(cmd, args[0], func(_ context.Context, _ *store.Tx, a *store.Action) error {
		if a.State != store.ActionSnoozed {
			return fmt.Errorf("%s is %s, not snoozed", a.ID, a.State)
		}
		a.State = state
		a.SnoozeUntil = sql.NullString{}
		a.SnoozeReason = ""
		return nil
	})
}

// updateAction is the read-modify-write every action verb performs.
func updateAction(cmd *cobra.Command, id string, change func(context.Context, *store.Tx, *store.Action) error) error {
	ctx := cmd.Context()

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

	before, err := tx.LoadAction(ctx, id)
	if err != nil {
		return notFoundOr(err, id)
	}

	after := before.Clone()
	if err := change(ctx, tx, after); err != nil {
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
		fmt.Fprintf(out, "%s unchanged\n", id)
		return nil
	}
	for _, change := range changes {
		fmt.Fprintf(out, "%s %s\n", id, change)
	}
	return nil
}

func newActionShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <action>",
		Short: "Print one action in full",
		Args:  cobra.ExactArgs(1),
		RunE:  runActionShow,
	}
	addOutputFlag(cmd)
	return cmd
}

func runActionShow(cmd *cobra.Command, args []string) error {
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

	a, err := tx.LoadAction(ctx, args[0])
	if err != nil {
		return notFoundOr(err, args[0])
	}

	if format == outputJSON {
		encoded, err := store.MarshalRecord(a)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}
	extra, err := actionEdgeRows(ctx, tx, a)
	if err != nil {
		return err
	}
	return writeRecordDetailWith(cmd.OutOrStdout(), a, extra)
}

// actionEdgeRows are what an action is waiting for and what it is about.
// Without them a blocked action shows a state and no reason for it.
func actionEdgeRows(ctx context.Context, tx *store.Tx, a *store.Action) ([][2]string, error) {
	var rows [][2]string

	blockers, err := tx.OpenBlockers(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	if len(blockers) > 0 {
		rows = append(rows, [2]string{"blocked_by", strings.Join(blockers, ", ")})
	}

	pr, ok, err := tx.SubjectPR(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	if ok {
		rows = append(rows, [2]string{"subject_pr", pr})
	}
	return rows, nil
}

func newActionListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Actions, in the order they were created",
		Long: "Creation order, not queue order: ranking needs the dependency graph,\n" +
			"which arrives with the queue.",
		Args: cobra.NoArgs,
		RunE: runActionList,
	}
	f := cmd.Flags()
	f.Bool("open", false, "not closed")
	f.Bool("expired", false, "snoozed with a date that has passed")
	f.String(flagStatus, "", "filter to one state")
	f.String(flagVerb, "", "filter to one verb")
	f.String(flagProject, "", "filter to one project")
	addOutputFlag(cmd)
	return cmd
}

func runActionList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	filter, err := actionFilterFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	actions, err := st.ListActions(ctx, filter)
	if err != nil {
		return err
	}

	if format == outputJSON {
		encoded, err := store.MarshalRecords(actions)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}
	return writeActionTable(cmd.OutOrStdout(), actions)
}

func actionFilterFrom(cmd *cobra.Command) (store.ActionFilter, error) {
	f := cmd.Flags()

	open, err := f.GetBool("open")
	if err != nil {
		return store.ActionFilter{}, err
	}
	expired, err := f.GetBool("expired")
	if err != nil {
		return store.ActionFilter{}, err
	}
	state, err := f.GetString(flagStatus)
	if err != nil {
		return store.ActionFilter{}, err
	}
	verb, err := f.GetString(flagVerb)
	if err != nil {
		return store.ActionFilter{}, err
	}
	project, err := f.GetString(flagProject)
	if err != nil {
		return store.ActionFilter{}, err
	}
	return store.ActionFilter{
		State: state, Verb: verb, Project: project, Open: open, Expired: expired,
	}, nil
}

func writeActionTable(out io.Writer, actions []*store.Action) error {
	if len(actions) == 0 {
		fmt.Fprintln(out, "no actions")
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATE\tVERB\tPROJECT\tSNOOZED UNTIL\tTITLE")
	for _, a := range actions {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			a.ID, a.State, a.Verb, nullText(a.ProjectID),
			nullText(a.SnoozeUntil), a.Title)
	}
	return w.Flush()
}

// The edges. Closing is still a stub: it walks these, and arrives with the
// cascade.

const (
	flagFrom   = "from"
	flagTo     = "to"
	flagAction = "action"
	flagBehind = "behind"
	flagPR     = "pr"
	flagRole   = "role"
	flagReason = "reason"
)

func newActionAddBlockerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add-blocker",
		Short: "Record that one action must precede another",
		Long: "The blocked action moves to blocked, and returns to ready when its\n" +
			"last open blocker closes. It still appears in the queue meanwhile,\n" +
			"with what it is waiting for named — that is the difference from\n" +
			"hide-behind.",
		Args: cobra.NoArgs,
		RunE: runActionAddBlocker,
	}
	f := cmd.Flags()
	f.String(flagFrom, "", "blocked action (required)")
	f.String(flagTo, "", "action that blocks it (required)")
	_ = cmd.MarkFlagRequired(flagFrom)
	_ = cmd.MarkFlagRequired(flagTo)
	addActorFlag(cmd)
	return cmd
}

func runActionAddBlocker(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()

	blockedID, err := f.GetString(flagFrom)
	if err != nil {
		return err
	}
	blockerID, err := f.GetString(flagTo)
	if err != nil {
		return err
	}

	return withActionTx(cmd, func(ctx context.Context, tx *store.Tx) error {
		blocked, err := loadAction(ctx, tx, blockedID)
		if err != nil {
			return err
		}
		blocker, err := loadAction(ctx, tx, blockerID)
		if err != nil {
			return err
		}
		if !blocker.IsOpen() {
			return fmt.Errorf("%s is already %s and blocks nothing", blocker.ID, blocker.State)
		}
		if err := tx.AddBlocker(ctx, blocker, blocked); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s is %s, waiting on %s\n",
			blocked.ID, blocked.State, blocker.ID)
		return nil
	})
}

func newActionHideBehindCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hide-behind",
		Short: "Fold an action out of the queue until another clears",
		Long: "Not the same edge as add-blocker. Blocked-by is a fact about ordering;\n" +
			"hidden-behind is the judgement that there is nothing to do about this\n" +
			"one other than clearing the other.\n\n" +
			"--behind \"\" brings it back.",
		Args: cobra.NoArgs,
		RunE: runActionHideBehind,
	}
	f := cmd.Flags()
	f.String(flagAction, "", "action to hide (required)")
	f.String(flagBehind, "", "action it hides behind; empty brings it back (required)")
	_ = cmd.MarkFlagRequired(flagAction)
	_ = cmd.MarkFlagRequired(flagBehind)
	addActorFlag(cmd)
	return cmd
}

func runActionHideBehind(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()

	id, err := f.GetString(flagAction)
	if err != nil {
		return err
	}
	behind, err := f.GetString(flagBehind)
	if err != nil {
		return err
	}

	return updateAction(cmd, id, func(ctx context.Context, tx *store.Tx, a *store.Action) error {
		if behind == "" {
			a.HiddenBehind = sql.NullString{}
			return nil
		}
		target, err := loadAction(ctx, tx, behind)
		if err != nil {
			return err
		}
		if !target.IsOpen() {
			return fmt.Errorf("%s is already %s; hiding behind it would hide %s for good",
				target.ID, target.State, a.ID)
		}
		a.HiddenBehind = sql.NullString{String: target.ID, Valid: true}
		return nil
	})
}

func newActionLinkPRCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link-pr",
		Short: "Attach a pull request to an action",
		Long: "An action has at most one subject pull request; --role context is for\n" +
			"mentioning one as background.\n\n" +
			"The subject is what closing reads: a predicate verb asks the pull\n" +
			"request whether its work is done, and a context link is not asked.",
		Args: cobra.NoArgs,
		RunE: runActionLinkPR,
	}
	f := cmd.Flags()
	f.String(flagAction, "", "action, e.g. NA103 (required)")
	f.String(flagPR, "", "pull request, e.g. owner/repo#812 (required)")
	f.String(flagRole, store.RoleSubject, "subject or context")
	_ = cmd.MarkFlagRequired(flagAction)
	_ = cmd.MarkFlagRequired(flagPR)
	addActorFlag(cmd)
	return cmd
}

func runActionLinkPR(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()

	id, err := f.GetString(flagAction)
	if err != nil {
		return err
	}
	prID, err := f.GetString(flagPR)
	if err != nil {
		return err
	}
	role, err := f.GetString(flagRole)
	if err != nil {
		return err
	}
	// Checked here as well as in the store, so that a typo is reported as
	// itself rather than as whatever the lookups happen to fail on first.
	if role != store.RoleSubject && role != store.RoleContext {
		return fmt.Errorf("--%s %q is not a role: use %s or %s",
			flagRole, role, store.RoleSubject, store.RoleContext)
	}

	return withActionTx(cmd, func(ctx context.Context, tx *store.Tx) error {
		a, err := loadAction(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.LoadPR(ctx, prID); err != nil {
			return notFoundOr(err, prID)
		}
		if err := tx.LinkPR(ctx, a, prID, role); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s\n", a.ID, role, prID)
		return nil
	})
}

// withActionTx runs body in one unit of work and commits it, which is the
// shape every command that writes something other than a single record needs.
func withActionTx(cmd *cobra.Command, body func(context.Context, *store.Tx) error) error {
	ctx := cmd.Context()

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

	if err := body(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// loadAction reads one action, reporting a missing one by name.
func loadAction(ctx context.Context, tx *store.Tx, id string) (*store.Action, error) {
	a, err := tx.LoadAction(ctx, id)
	if err != nil {
		return nil, notFoundOr(err, id)
	}
	return a, nil
}

func newActionCloseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "close <action>",
		Short: "Close an action; cascades to its dependents",
		Long: "Closing is where the queue moves on its own. It instantiates the\n" +
			"repository's pipeline when the verb starts one, frees whatever this\n" +
			"action was blocking, and brings back anything hidden behind it — all\n" +
			"under one correlation id, so the log reads as a single act.\n\n" +
			"Steps the pull request already satisfies are skipped rather than\n" +
			"created complete.",
		Args: cobra.ExactArgs(1),
		RunE: runActionClose,
	}
	f := cmd.Flags()
	f.String(flagReason, store.ClosedCompleted, strings.Join(store.ClosedReasons, ", "))
	f.String(flagPR, "", "link a pull request as the subject while closing")
	addActorFlag(cmd)
	return cmd
}

func runActionClose(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	f := cmd.Flags()

	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	reason, err := f.GetString(flagReason)
	if err != nil {
		return err
	}
	pr, err := f.GetString(flagPR)
	if err != nil {
		return err
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	result, err := st.CloseAction(ctx, actor, store.CloseRequest{
		ID: args[0], Reason: reason, PR: pr,
	})
	if err != nil {
		return notFoundOr(err, args[0])
	}
	return writeCloseResult(cmd.OutOrStdout(), result)
}

// writeCloseResult reports the cascade. Everything it lists happened in the
// same unit of work, and saying so is most of the value: a close that quietly
// created four actions somewhere else would be indistinguishable from a bug.
func writeCloseResult(out io.Writer, r *store.CloseResult) error {
	fmt.Fprintf(out, "%s %s (%s)\n", r.Closed.ID, r.Closed.State, nullText(r.Closed.ClosedReason))

	for _, verb := range r.Skipped {
		fmt.Fprintf(out, "  skipped %s: already true\n", verb)
	}
	for i, a := range r.Created {
		blocked := ""
		if i > 0 {
			blocked = fmt.Sprintf(" (blocked by %s)", r.Created[i-1].ID)
		}
		fmt.Fprintf(out, "  created %s %s%s\n", a.ID, a.Title, blocked)
	}
	for _, a := range r.Unblocked {
		fmt.Fprintf(out, "  %s is now %s\n", a.ID, a.State)
	}
	for _, a := range r.Unhidden {
		fmt.Fprintf(out, "  %s is no longer hidden\n", a.ID)
	}
	return nil
}
