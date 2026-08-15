package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

const (
	flagVerb       = "verb"
	flagProject    = "project"
	flagWhy        = "why"
	flagRankPin    = "rank-pin"
	flagOkayToWait = "okay-to-wait-until"
	flagWaitingFor = "waiting-for"
)

func newActionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "action",
		Short: "One thing to do next, allocated an NA identifier",
		Long: "An action's verb decides how it closes. A predicate verb closes when\n" +
			"GitHub says the work is done; a human verb closes when you say so, and\n" +
			"those are the only ones that reach the queue as thinking work.\n\n" +
			"`roz verb list` shows the vocabulary.",
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
		newActionWaitRefCmd(),
		newActionCloseCmd(),
	)
	return cmd
}

// actionFieldFlags are the authored columns settable from the command line.
// closed_at and closed_reason are absent: `action close` sets those, with the
// cascade that belongs to them.
var actionFieldFlags = []string{
	flagTitle, flagVerb, flagProject, flagWhy, flagStatus, flagRankPin,
	flagSnoozeUntil, flagSnoozeReason, flagOkayToWait, flagWaitingFor,
}

func addActionFieldFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String(flagTitle, "", "what to do")
	f.String(flagVerb, "", "from the vocabulary; see `roz verb list`")
	f.String(flagProject, "", "project this advances, e.g. ROZ200; omit if it advances nothing")
	f.String(flagWhy, "", "what it unblocks; one sentence maximum")
	f.String(flagStatus, "", strings.Join(store.ActionStates, ", "))
	f.Int(flagRankPin, 0, "manual sort override; 0 clears it")
	f.String(flagSnoozeUntil, "", "ISO-8601 date or timestamp; requires state snoozed")
	f.String(flagSnoozeReason, "", "why it is deferred")
	f.String(flagOkayToWait, "",
		"when waiting on this stops being reasonable; empty clears it and the verb's allowance applies")
	f.String(flagWaitingFor, "",
		"the one owner whose review actually unblocks this, e.g. @org/storage; empty clears it")
}

func newActionAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Allocate a new action and print its id",
		Args:  cobra.NoArgs,
		RunE:  runActionAdd,
	}
	addActionFieldFlags(cmd)
	// A predicate verb needs a subject to evaluate, so the pull request has to
	// be nameable in the same command that chooses the verb. Without this the
	// check below would refuse a legitimate add and offer a two-step fix.
	cmd.Flags().String(flagPR, "", "the pull request this action is about, e.g. owner/repo#812")
	// And for the same reason, a verb that closes on a ref needs to be able to
	// say which one in the command that chose it. See `action wait-ref`.
	addRefWaitFlags(cmd)
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

	st, err := openStore(cmd)
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
	prID, err := f.GetString(flagPR)
	if err != nil {
		return err
	}
	intent, err := refIntentFrom(cmd, "")
	if err != nil {
		return err
	}
	if err := checkActionReferences(ctx, st, actor, a, prID, intent); err != nil {
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
	// After the insert, so the link has a row to point at. Subject rather than
	// context: this is the pull request the action is about, which is what a
	// predicate asks.
	if prID != "" {
		if err := tx.LinkPR(ctx, a, prID, store.RoleSubject); err != nil {
			return err
		}
	}
	if intent != nil {
		intent.setActionID(a.ID)
		if err := intent.record(ctx, tx); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout(), a.ID)
	if prID == "" && intent == nil {
		return nil
	}
	// A gate has no version until the repository's releases have been read,
	// and settling one that has not resolved would be asking whether an action
	// is waiting for something nobody has worked out yet.
	//
	// Reported after the identifier, the way a settled action already is: what
	// a gate came out as is not visible anywhere else, and working it out is
	// roz's decision rather than something the caller wrote.
	resolved, err := resolveGateNow(ctx, st, intent)
	if err != nil {
		return err
	}
	reportGate(cmd, intent, resolved)
	// An action created against a pull request that already satisfies its
	// predicate is the same staleness `link-pr` had: born ready, and closing
	// only at the next poll.
	return settleActionNow(ctx, cmd, st, a.ID)
}

// checkActionReferences confirms the verb and project exist, in a
// transaction that is finished with before anything else runs.
func checkActionReferences(ctx context.Context, st *store.Store, actor store.Actor, a *store.Action, prID string, intent *refIntent) error {
	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	v, err := loadUsableVerb(ctx, tx, a.Verb)
	if err != nil {
		return err
	}
	if err := checkPredicateHasSubject(v, prID != "", "pass --"+flagPR); err != nil {
		return err
	}
	if err := checkPredicateHasRefWait(v, intent != nil, "pass --"+flagRefRepo+" and --"+flagRef); err != nil {
		return err
	}
	if intent != nil {
		if _, err := tx.LoadGitHubRepo(ctx, intent.repoID()); err != nil {
			return notFoundOr(err, intent.repoID())
		}
	}
	if prID != "" {
		if _, err := tx.LoadPR(ctx, prID); err != nil {
			return notFoundOr(err, prID)
		}
	}
	return checkProjectExists(ctx, tx, a.ProjectID)
}

// checkVerbUsable reports an unknown or retired verb as itself, rather than
// letting the foreign key report a constraint.
func checkVerbUsable(ctx context.Context, tx *store.Tx, verb string) error {
	_, err := loadUsableVerb(ctx, tx, verb)
	return err
}

func loadUsableVerb(ctx context.Context, tx *store.Tx, verb string) (*store.ActionVerb, error) {
	v, err := tx.LoadVerb(ctx, verb)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%q is not a verb; see `roz verb list`", verb)
	}
	if err != nil {
		return nil, err
	}
	if !v.Active {
		return nil, fmt.Errorf("%q is retired and cannot be used for new actions", verb)
	}
	return v, nil
}

// checkPredicateHasSubject refuses an action that could never close.
//
// A predicate verb closes by asking its pull request a question. With no
// subject there is nothing to ask, and the answer is false forever — correctly,
// since absence is not completion — so the action sits in the queue describing
// work that may well be finished, with nothing on it saying why it will not
// move.
//
// Keyed on closes = predicate rather than on requires_pr alone: `review`
// carries a pull request and still closes on a person, so it is unaffected.
// Both conditions, so a future predicate that asks something other than a pull
// request is not caught by a rule about pull requests.
// remedy differs by command: `action add` can take the pull request inline,
// while `action set` has no such flag and wants `action link-pr`. An error
// naming a flag the command does not have is its own small bug.
func checkPredicateHasSubject(v *store.ActionVerb, hasPR bool, remedy string) error {
	if hasPR || v.Closes != store.ClosesPredicate || !v.RequiresPR {
		return nil
	}
	return fmt.Errorf(
		"%q closes when %s says so, so it needs a pull request to ask: %s, "+
			"or use a verb that closes on a person and let closing it open the chain",
		v.Verb, v.PredicateKey.String, remedy)
}

// checkPredicateHasRefWait is the same refusal for a verb that closes on a
// ref rather than on a pull request.
//
// Separate from checkPredicateHasSubject because the remedy differs, which is
// the whole reason requires_ref is its own column: an error telling somebody
// to link a pull request to a wait_ref action would send them the wrong way.
func checkPredicateHasRefWait(v *store.ActionVerb, hasWait bool, remedy string) error {
	if hasWait || v.Closes != store.ClosesPredicate || !v.RequiresRef {
		return nil
	}
	return fmt.Errorf(
		"%q closes when a matching ref appears, so it needs to know which: %s",
		v.Verb, remedy)
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
	if f.Changed(flagOkayToWait) {
		v, err := f.GetString(flagOkayToWait)
		if err != nil {
			return err
		}
		// Empty clears it, and the verb's allowance applies again. Unlike a
		// snooze this needs no matching state: an action is not "waiting"
		// as a state, it is waiting because its verb says so.
		if v == "" {
			a.OkayToWaitUntil = sql.NullString{}
		} else {
			at, err := validateTimestamp(flagOkayToWait, v)
			if err != nil {
				return err
			}
			a.OkayToWaitUntil = sql.NullString{String: at, Valid: true}
		}
	}
	if f.Changed(flagWaitingFor) {
		v, err := f.GetString(flagWaitingFor)
		if err != nil {
			return err
		}
		// Empty clears it, which is what happens when the team it named
		// approves and the wait moves on. Going stale is the expected failure
		// here, so correcting it has to be cheap.
		if strings.TrimSpace(v) == "" {
			a.WaitingFor = sql.NullString{}
		} else {
			owner, err := oneOwner(flagWaitingFor, v)
			if err != nil {
				return err
			}
			a.WaitingFor = sql.NullString{String: owner, Valid: true}
		}
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
		return fmt.Errorf("state %s needs a date: use `roz action snooze %s --%s <date>`",
			store.ActionSnoozed, a.ID, flagSnoozeUntil)
	case dated && !snoozed:
		return fmt.Errorf("a snooze date needs state %s: use `roz action snooze %s --%s <date>`",
			store.ActionSnoozed, a.ID, flagSnoozeUntil)
	}

	// closed_at and state move together; `action close` is what sets them.
	closed := a.ClosedAt.Valid
	finished := a.State == store.ActionDone || a.State == store.ActionDropped
	if closed != finished {
		return fmt.Errorf("state %s cannot be set here: use `roz action close %s`", a.State, a.ID)
	}
	return nil
}

func newActionSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <action>",
		Short: "Change authored columns on an existing action",
		Long: "Only the columns given are touched. Closing is not one of them —\n" +
			"`roz action close` does that, with the cascade that belongs to it.",
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
			v, err := loadUsableVerb(ctx, tx, a.Verb)
			if err != nil {
				return err
			}
			// Moving an action onto a predicate verb has the same requirement
			// as creating one there, and the subject it needs may already be
			// linked.
			_, hasPR, err := tx.SubjectPR(ctx, a.ID)
			if err != nil {
				return err
			}
			if err := checkPredicateHasSubject(v, hasPR, "link one with `roz action link-pr`"); err != nil {
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
			"quiet. `roz action list --expired` is what finds them again.",
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

	// Changing the verb changes which question the action closes on, and the
	// answer may already be yes. #111 stopped `add` and `set` creating a
	// predicate action with no subject, so this is now the way an action
	// arrives at a satisfied predicate without a poll in between.
	if verbChanged(changes) {
		return settleActionNow(ctx, cmd, st, id)
	}
	return nil
}

func verbChanged(changes []store.Change) bool {
	for _, c := range changes {
		if c.Column == flagVerb {
			return true
		}
	}
	return false
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

	a, err := tx.LoadAction(ctx, args[0])
	if err != nil {
		return notFoundOr(err, args[0])
	}

	return showRecord(cmd, ctx, tx, a, format)
}

func newActionListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Actions, in the order they were created",
		Long: "Creation order by default. --sort priority uses what the status page\n" +
			"uses: any rank_pin, then the priority of the project the action\n" +
			"advances, then the verb's rank class, then how much closing it would\n" +
			"free, then effort. See the README for what sets each of those.",
		Args: cobra.NoArgs,
		RunE: runActionList,
	}
	f := cmd.Flags()
	f.Bool("open", false, "not closed")
	f.Bool("unblocked", false, "ready, not hidden, not waiting on anyone — the queue")
	f.Bool("waiting", false, "what the queue leaves out because it waits on somebody")
	f.Bool("stale", false, "closed as completed while the pull request is still open")
	f.Bool("expired", false, "snoozed with a date that has passed")
	f.String(flagStatus, "", "filter to one state")
	f.String(flagVerb, "", "filter to one verb")
	f.String(flagProject, "", "filter to one project")
	addSortFlag(cmd, actionColumns)
	addListingFlags(cmd, actionColumns)
	return cmd
}

func runActionList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	filter, err := actionFilterFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	actions, err := st.ListActions(ctx, filter)
	if err != nil {
		return err
	}
	// How late each one is, so an allowance on a verb whose actions stay in
	// the queue means something visible. Those get a mark rather than a second
	// item about themselves, and the column is where the mark lands.
	//
	// Read whatever the output is, now that the column can be asked for by
	// name. One query, against the same rows that were being listed anyway.
	late, err := st.LateActions(ctx)
	if err != nil {
		return err
	}
	// Which projects are blocked, so an action held out of the queue says so
	// rather than sitting in the listing looking ready.
	blocked, err := st.BlockedProjects(ctx)
	if err != nil {
		return err
	}
	return runListing(cmd, actionColumns, actions,
		renderContext{late: late, blocked: blocked})
}

// actionColumns is what `action list` can show.
//
// LATE is the one column with nothing behind it in the record: how late an
// action is comes from its verb's allowance and the clock, and lives in a map
// beside the rows rather than in a column of action.
var actionColumns = columnSet[*store.Action]{
	blank: &store.Action{},
	declared: []column[*store.Action]{
		{name: "project_id", header: "PROJECT"},
		{
			name: "snooze_until", header: "SNOOZED UNTIL",
			render: func(a *store.Action, ctx renderContext) string {
				return snoozeCell(a.SnoozeUntil, ctx.now)
			},
		},
		{
			name: "late",
			render: func(a *store.Action, ctx renderContext) string {
				return lateCell(ctx.late, a.ID)
			},
		},
		{
			// An action whose project is blocked is not in the queue, and a
			// listing that showed it looking ready would be the same two
			// views disagreeing that scottlaird/roz#181 was about.
			//
			// A mark rather than a name: which project it is, is the PROJECT
			// column beside it, and what is holding that project up is
			// `action show`, where there is room to name them.
			name: "held", header: "HELD",
			render: func(a *store.Action, ctx renderContext) string {
				if a.ProjectID.Valid && ctx.blocked[a.ProjectID.String] {
					return "yes"
				}
				return ""
			},
			showIf: func(rows []*store.Action, ctx renderContext) bool {
				for _, a := range rows {
					if a.ProjectID.Valid && ctx.blocked[a.ProjectID.String] {
						return true
					}
				}
				return false
			},
		},
		{
			// Shown when somebody has said who the wait is for, and otherwise
			// not — an empty column across every row is the listing asking a
			// question rather than answering one, and this one is answered by
			// hand until CODEOWNERS can default it.
			name: "waiting_for",
			showIf: func(rows []*store.Action, _ renderContext) bool {
				for _, a := range rows {
					if a.WaitingFor.Valid {
						return true
					}
				}
				return false
			},
		},
	},
	defaults: []string{"id", "state", "verb", "project_id", "snooze_until", "late", "held", "waiting_for", "title"},
	rankings: queueRankings,
	empty:    "no actions",
}

func actionFilterFrom(cmd *cobra.Command) (store.ActionFilter, error) {
	f := cmd.Flags()

	open, err := f.GetBool("open")
	if err != nil {
		return store.ActionFilter{}, err
	}
	unblocked, err := f.GetBool("unblocked")
	if err != nil {
		return store.ActionFilter{}, err
	}
	waiting, err := f.GetBool("waiting")
	if err != nil {
		return store.ActionFilter{}, err
	}
	stale, err := f.GetBool("stale")
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
	order, sort, err := sortFrom(cmd, actionColumns)
	if err != nil {
		return store.ActionFilter{}, err
	}
	return store.ActionFilter{
		State: state, Verb: verb, Project: project,
		Open: open, Unblocked: unblocked, Waiting: waiting,
		Expired: expired, Stale: stale, Order: order, Sort: sort,
	}, nil
}

// lateCell renders how far past its allowance an action is, and empty when it
// is not late at all.
//
// Presence in the map is what says late, not the number: a deadline missed an
// hour ago is nought days past it and still missed, and rendering that as "not
// late" would hide the first day of every one of these.
func lateCell(late map[string]int, id string) string {
	days, ok := late[id]
	switch {
	case !ok:
		return ""
	case days == 0:
		return "today"
	case days == 1:
		return "1 day"
	default:
		return fmt.Sprintf("%d days", days)
	}
}

// The edges. Closing walks these: see close.go for the cascade.

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

	return withActionTxThen(cmd, func(ctx context.Context, tx *store.Tx) error {
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
	}, func(ctx context.Context, st *store.Store) error {
		// Supplying the subject is the moment the predicate becomes
		// answerable, so ask now rather than leaving the queue stale until
		// the next poll. A context link is never asked, so nothing to settle.
		if role != store.RoleSubject {
			return nil
		}
		return settleActionNow(ctx, cmd, st, id)
	})
}

// settleActionNow closes one action if the fact just recorded satisfied it,
// and reports it the way sync does.
func settleActionNow(ctx context.Context, cmd *cobra.Command, st *store.Store, id string) error {
	settled, err := st.SettleAction(ctx, store.ActorPredicate, id)
	if err != nil {
		return err
	}
	reportSettled(cmd.OutOrStdout(), settled)
	return nil
}

// withActionTx runs body in one unit of work and commits it, which is the
// shape every command that writes something other than a single record needs.
func withActionTx(cmd *cobra.Command, body func(context.Context, *store.Tx) error) error {
	return withActionTxThen(cmd, body, nil)
}

// withActionTxThen adds a step that runs after the commit, for work that needs
// transactions of its own.
//
// Settling is the case: it closes under the predicate actor rather than the
// caller's, and a cascade that fails should not undo the fact that was
// recorded. Sync and `pr announce` settle after their write for the same
// reason.
func withActionTxThen(cmd *cobra.Command,
	body func(context.Context, *store.Tx) error,
	after func(context.Context, *store.Store) error) error {
	ctx := cmd.Context()

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

	if err := body(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if after == nil {
		return nil
	}
	return after(ctx, st)
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

	st, err := openStore(cmd)
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
	for _, a := range r.StoodDown {
		fmt.Fprintf(out, "  %s stood down: %s\n", a.ID, a.Title)
	}
	return nil
}
