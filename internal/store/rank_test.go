package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// ranked lists in the sketch's order and returns the ids, which is what every
// test here is really asserting about.
func ranked(t *testing.T, st *Store, filter ActionFilter) []string {
	t.Helper()
	filter.Order = OrderPriority

	actions, err := st.ListActions(context.Background(), filter)
	if err != nil {
		t.Fatalf("ListActions() returned error: %v", err)
	}
	ids := make([]string, len(actions))
	for i, a := range actions {
		ids[i] = a.ID
	}
	return ids
}

func wantOrder(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("ranked %v, want %v", got, want)
	}
}

// setEffort states how big a project is, which is the ranking's last term
// before creation order. setPriority next door states the other one.
func setEffort(t *testing.T, st *Store, p *Project, effort string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := p.Clone()
	after.Effort = sql.NullString{String: effort, Valid: true}
	if _, err := tx.Update(ctx, p, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	*p = *after
}

// advances points an action at a project.
func advances(t *testing.T, st *Store, a *Action, p *Project) {
	t.Helper()
	attach(t, st, a, func(a *Action) {
		a.ProjectID = sql.NullString{String: p.ID, Valid: true}
	})
}

// TestRankClassOrdersTheQueue: one click before a judgement before real work
// before waiting on somebody. Created in the reverse of the wanted order, so
// creation order cannot produce a passing result by accident.
func TestRankClassOrdersTheQueue(t *testing.T) {
	st := newStore(t)

	waiting := addAction(t, st, "wait for review", "wait_review")
	work := addAction(t, st, "write it", "write")
	judgement := addAction(t, st, "decide the shape", "decide")
	click := addAction(t, st, "run the job", "run")

	wantOrder(t, ranked(t, st, ActionFilter{Open: true}),
		[]string{click.ID, judgement.ID, work.ID, waiting.ID})
}

// TestUnblocksCountIsTransitive is the whole reason the term exists. The
// sketch wanted "the ones gating chains", and a direct count cannot see a
// chain at all.
//
// The two cases are chosen so the counts disagree about the winner rather
// than merely differing: wide blocks two leaves directly, deep heads a chain
// of three. Direct says wide (2) beats deep (1); transitive says deep (3)
// beats wide (2). wide is created first, so creation order cannot rescue the
// transitive answer either.
func TestUnblocksCountIsTransitive(t *testing.T) {
	st := newStore(t)

	wide := addAction(t, st, "blocks two leaves", "write")
	for _, title := range []string{"leaf one", "leaf two"} {
		blockOn(t, st, addAction(t, st, title, "write"), wide)
	}

	deep := addAction(t, st, "heads a chain of three", "write")
	previous := deep
	for _, title := range []string{"first", "second", "third"} {
		next := addAction(t, st, title, "write")
		blockOn(t, st, next, previous)
		previous = next
	}

	// Neither is pinned or on a project, so unblocks is the only term that
	// can separate them.
	wantOrder(t, ranked(t, st, ActionFilter{State: ActionReady}),
		[]string{deep.ID, wide.ID})
}

// TestUnblocksCountIgnoresClosedFollowers: freeing something already done is
// worth nothing, and a finished chain would otherwise inflate its head for
// ever.
func TestUnblocksCountIgnoresClosedFollowers(t *testing.T) {
	st := newStore(t)

	stale := addAction(t, st, "gates a finished chain", "write")
	doneFollower := addAction(t, st, "already done", "write")
	blockOn(t, st, doneFollower, stale)

	live := addAction(t, st, "gates live work", "write")
	openFollower := addAction(t, st, "still to do", "write")
	blockOn(t, st, openFollower, live)

	// Close the first chain's follower. Its blocker now frees nothing.
	closeIt(t, st, CloseRequest{ID: doneFollower.ID})

	got := ranked(t, st, ActionFilter{State: ActionReady})
	wantOrder(t, got, []string{live.ID, stale.ID})
}

// TestEffortBreaksTheTie: everything else equal, prefer the thing that
// finishes. The effort is the project's, which is the only one the schema
// has — an action has no size of its own beyond its rank class.
func TestEffortBreaksTheTie(t *testing.T) {
	st := newStore(t)

	long := insertProject(t, st, "weeks of it")
	setPriority(t, st, long, 1)
	setEffort(t, st, long, EffortWeeks)
	short := insertProject(t, st, "an afternoon")
	setPriority(t, st, short, 1)
	setEffort(t, st, short, EffortHours)

	slow := addAction(t, st, "the long one", "write")
	advances(t, st, slow, long)
	quick := addAction(t, st, "the short one", "write")
	advances(t, st, quick, short)

	wantOrder(t, ranked(t, st, ActionFilter{State: ActionReady}),
		[]string{quick.ID, slow.ID})
}

// TestPriorityBeatsRankClass is the one departure from the sketch, and the
// author's call: a one-click action on a barely-wanted project must not
// outrank real work on the most wanted one.
func TestPriorityBeatsRankClass(t *testing.T) {
	st := newStore(t)

	wanted := insertProject(t, st, "the important one")
	setPriority(t, st, wanted, 1)
	barely := insertProject(t, st, "the barely-wanted one")
	setPriority(t, st, barely, 4)

	realWork := addAction(t, st, "write the endpoint", "write")
	advances(t, st, realWork, wanted)
	oneClick := addAction(t, st, "run the job", "run")
	advances(t, st, oneClick, barely)

	wantOrder(t, ranked(t, st, ActionFilter{State: ActionReady}),
		[]string{realWork.ID, oneClick.ID})
}

// TestRankPinBeatsEverything: it exists to override whatever the system
// worked out, so it has to survive every other term disagreeing.
func TestRankPinBeatsEverything(t *testing.T) {
	st := newStore(t)

	wanted := insertProject(t, st, "priority one, quick, and clicky")
	setPriority(t, st, wanted, 1)
	setEffort(t, st, wanted, EffortMinutes)

	favoured := addAction(t, st, "everything says do this first", "run")
	advances(t, st, favoured, wanted)

	pinned := addAction(t, st, "but do this one first", "write")
	attach(t, st, pinned, func(a *Action) {
		a.RankPin = sql.NullInt64{Int64: 1, Valid: true}
	})

	wantOrder(t, ranked(t, st, ActionFilter{State: ActionReady}),
		[]string{pinned.ID, favoured.ID})
}

// TestUnstatedSortsLast: unprioritised is not the same as low, and an effort
// nobody estimated is not evidence of being quick.
func TestUnstatedSortsLast(t *testing.T) {
	st := newStore(t)

	stated := insertProject(t, st, "priority four, but stated")
	setPriority(t, st, stated, 4)

	said := addAction(t, st, "on a project with a priority", "write")
	advances(t, st, said, stated)
	unsaid := addAction(t, st, "on nothing at all", "write")

	wantOrder(t, ranked(t, st, ActionFilter{State: ActionReady}),
		[]string{said.ID, unsaid.ID})
}

// TestCreationOrderIsUntouched: the ranking is opt-in, and the default is
// still honest about being arbitrary.
func TestCreationOrderIsUntouched(t *testing.T) {
	st := newStore(t)

	first := addAction(t, st, "wait for review", "wait_review")
	second := addAction(t, st, "one click", "run")

	actions, err := st.ListActions(context.Background(), ActionFilter{Open: true})
	if err != nil {
		t.Fatalf("ListActions() returned error: %v", err)
	}
	if len(actions) != 2 || actions[0].ID != first.ID || actions[1].ID != second.ID {
		t.Errorf("default order changed; want %s then %s", first.ID, second.ID)
	}
}

// TestAProjectlessActionTakesTheMiddleBand is #257. An action advancing no
// project had no priority to compare and lost to every action that had one,
// whatever its verb and whatever it would unblock: on a queue of thirteen the
// bottom four were exactly the four with no project.
func TestAProjectlessActionTakesTheMiddleBand(t *testing.T) {
	st := newStore(t)

	urgent := addProject(t, st, "urgent")
	barely := addProject(t, st, "barely wanted")
	setPriority(t, st, urgent, 1)
	setPriority(t, st, barely, 8)

	onUrgent := addAction(t, st, "write the urgent one", "write")
	onBarely := addAction(t, st, "write the barely wanted one", "write")
	loose := addAction(t, st, "decide something", "decide")
	advances(t, st, onUrgent, urgent)
	advances(t, st, onBarely, barely)

	got := ranked(t, st, ActionFilter{})
	want := []string{onUrgent.ID, loose.ID, onBarely.ID}
	wantOrder(t, got, want)
}

// TestTheFillIsABandAProjectCanHold. A sentinel outside 1..9 would read as a
// real band in anything that groups by priority, and a band nobody can be in
// is worse than a wrong one.
func TestTheFillIsABandAProjectCanHold(t *testing.T) {
	if unstatedPriority < 1 || unstatedPriority > 9 {
		t.Errorf("unstatedPriority = %d, which no project can hold", unstatedPriority)
	}
}

// TestRankPinStillBeatsEverything, which is the escape hatch for exactly this
// kind of disagreement and must not have been narrowed by filling the band.
func TestRankPinStillBeatsEverything(t *testing.T) {
	st := newStore(t)

	urgent := addProject(t, st, "urgent")
	setPriority(t, st, urgent, 1)
	onUrgent := addAction(t, st, "write the urgent one", "write")
	advances(t, st, onUrgent, urgent)

	pinned := addAction(t, st, "pinned, and advancing nothing", "write")
	attach(t, st, pinned, func(a *Action) {
		a.RankPin = sql.NullInt64{Int64: 1, Valid: true}
	})

	if got := ranked(t, st, ActionFilter{}); got[0] != pinned.ID {
		t.Errorf("order = %v, want %s first", got, pinned.ID)
	}
}

// TestAnUnstatedEffortStillSortsLast. Only the priority term is filled: an
// effort nobody has estimated is not evidence of being quick, where a missing
// priority is evidence of nothing at all — which is what the middle means.
func TestAnUnstatedEffortStillSortsLast(t *testing.T) {
	st := newStore(t)

	quick := addProject(t, st, "quick")
	unknown := addProject(t, st, "unestimated")
	setPriority(t, st, quick, 5)
	setPriority(t, st, unknown, 5)
	setEffort(t, st, quick, "hours")

	a := addAction(t, st, "on the quick one", "write")
	b := addAction(t, st, "on the unestimated one", "write")
	advances(t, st, a, quick)
	advances(t, st, b, unknown)

	got := ranked(t, st, ActionFilter{})
	wantOrder(t, got, []string{a.ID, b.ID})
}
