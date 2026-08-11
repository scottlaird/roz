package store

import (
	"context"
	"strings"
	"testing"
)

// applyChecks records one poll's worth of check results.
func applyChecks(t *testing.T, st *Store, prID string, observed map[string]string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.ApplyChecks(ctx, prID, observed); err != nil {
		t.Fatalf("ApplyChecks() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

func storedChecks(t *testing.T, st *Store, prID string) string {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	checks, err := tx.ChecksFor(ctx, prID)
	if err != nil {
		t.Fatalf("ChecksFor() returned error: %v", err)
	}
	return FormatChecks(checks)
}

// checkEvents returns the logged check transitions as "field: old → new".
func checkEvents(t *testing.T, st *Store) []string {
	t.Helper()

	events, err := st.Events(context.Background(), EventQuery{Kind: eventChanged})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	var logged []string
	for _, e := range events {
		if strings.HasPrefix(e.Field, "checks/") {
			logged = append(logged, e.Field+": "+e.OldValue+" → "+e.NewValue)
		}
	}
	return logged
}

func trackedPR(t *testing.T, st *Store) string {
	t.Helper()
	trackRepo(t, st, "scottlaird/todo")
	return trackPR(t, st, "scottlaird/todo", 1).ID
}

// TestGoingGreenIsWrittenNotLogged is the whole of SL25. Two pull requests
// produced about fifteen `checks` events in an hour and none of them said
// anything: almost every transition is something going green.
//
// The same argument pr.last_synced_at already makes as an auto column —
// written because it is true, unlogged because it would bury what matters.
func TestGoingGreenIsWrittenNotLogged(t *testing.T) {
	st := newStore(t)
	pr := trackedPR(t, st)

	applyChecks(t, st, pr, map[string]string{"build": "PENDING", "test": "PENDING"})
	applyChecks(t, st, pr, map[string]string{"build": "SUCCESS", "test": "SUCCESS"})

	if got := storedChecks(t, st, pr); got != "build SUCCESS, test SUCCESS" {
		t.Errorf("stored = %q, want both green", got)
	}
	if logged := checkEvents(t, st); len(logged) != 0 {
		t.Errorf("going green logged %v, want nothing", logged)
	}
}

// TestBreakingIsLoggedByName: the payload worth having is which check broke,
// which the blob could never say — it could only report that the map changed.
func TestBreakingIsLoggedByName(t *testing.T) {
	st := newStore(t)
	pr := trackedPR(t, st)

	applyChecks(t, st, pr, map[string]string{"build": "SUCCESS", "test": "SUCCESS"})
	applyChecks(t, st, pr, map[string]string{"build": "SUCCESS", "test": "FAILURE"})

	logged := checkEvents(t, st)
	want := []string{"checks/test: SUCCESS → FAILURE"}
	if !equalStrings(logged, want) {
		t.Errorf("logged %v, want %v", logged, want)
	}
}

// TestStillBrokenIsNotNews is the distinction the blob could not draw: any
// change to any check re-emitted the lot, so a failure that had been there
// for an hour looked identical to one that had just appeared.
func TestStillBrokenIsNotNews(t *testing.T) {
	st := newStore(t)
	pr := trackedPR(t, st)

	applyChecks(t, st, pr, map[string]string{"build": "FAILURE", "test": "PENDING"})
	before := len(checkEvents(t, st))

	// build is still broken; test finishes. Neither is news.
	applyChecks(t, st, pr, map[string]string{"build": "FAILURE", "test": "SUCCESS"})

	if after := len(checkEvents(t, st)); after != before {
		t.Errorf("a still-broken check logged again: %v", checkEvents(t, st))
	}
}

// TestRecoveringIsLogged: crossing back out of trouble is as much news as
// crossing into it, and is how a monitor knows to stop caring.
func TestRecoveringIsLogged(t *testing.T) {
	st := newStore(t)
	pr := trackedPR(t, st)

	applyChecks(t, st, pr, map[string]string{"build": "FAILURE"})
	applyChecks(t, st, pr, map[string]string{"build": "SUCCESS"})

	want := []string{
		"checks/build:  → FAILURE",
		"checks/build: FAILURE → SUCCESS",
	}
	if logged := checkEvents(t, st); !equalStrings(logged, want) {
		t.Errorf("logged %v, want %v", logged, want)
	}
}

// TestAChecksDisappearanceIsNotAFailure: contexts come and go with the
// workflow file, and a check nobody runs any more is not a check that failed.
func TestAChecksDisappearanceIsNotAFailure(t *testing.T) {
	st := newStore(t)
	pr := trackedPR(t, st)

	applyChecks(t, st, pr, map[string]string{"build": "SUCCESS", "retired": "SUCCESS"})
	before := len(checkEvents(t, st))

	applyChecks(t, st, pr, map[string]string{"build": "SUCCESS"})

	if got := storedChecks(t, st, pr); got != "build SUCCESS" {
		t.Errorf("stored = %q, want the retired check gone", got)
	}
	if after := len(checkEvents(t, st)); after != before {
		t.Errorf("a green check disappearing logged %v", checkEvents(t, st))
	}
}

// TestABrokenCheckDisappearingIsAnAnswer: it stops being a question, and the
// log should say so rather than leaving the last word as FAILURE.
func TestABrokenCheckDisappearingIsAnAnswer(t *testing.T) {
	st := newStore(t)
	pr := trackedPR(t, st)

	applyChecks(t, st, pr, map[string]string{"flaky": "FAILURE"})
	applyChecks(t, st, pr, map[string]string{})

	want := []string{
		"checks/flaky:  → FAILURE",
		"checks/flaky: FAILURE → ",
	}
	if logged := checkEvents(t, st); !equalStrings(logged, want) {
		t.Errorf("logged %v, want %v", logged, want)
	}
}

// TestPendingIsNotBroken: a check that has not finished is not a failure, and
// treating it as one would raise something on every push.
func TestPendingIsNotBroken(t *testing.T) {
	st := newStore(t)
	pr := trackedPR(t, st)

	applyChecks(t, st, pr, map[string]string{"build": "PENDING"})

	if logged := checkEvents(t, st); len(logged) != 0 {
		t.Errorf("a pending check logged %v, want nothing", logged)
	}
}

// TestEveryBrokenStateCounts: GitHub has more than one way to say a check is
// not passing, and a cancelled job is as much a reason to look as a failed one.
func TestEveryBrokenStateCounts(t *testing.T) {
	for _, state := range brokenCheckStates {
		t.Run(state, func(t *testing.T) {
			st := newStore(t)
			pr := trackedPR(t, st)

			applyChecks(t, st, pr, map[string]string{"build": state})
			if logged := checkEvents(t, st); len(logged) != 1 {
				t.Errorf("%s logged %v, want one event", state, logged)
			}
		})
	}
}

// TestChecksSurviveAName: names come from GitHub and are not identifiers of
// ours, so a name with a slash or a space in it must round-trip.
func TestChecksSurviveAName(t *testing.T) {
	st := newStore(t)
	pr := trackedPR(t, st)

	name := "build / test (ubuntu-latest)"
	applyChecks(t, st, pr, map[string]string{name: "SUCCESS"})

	if got := storedChecks(t, st, pr); got != name+" SUCCESS" {
		t.Errorf("stored = %q, want %q", got, name+" SUCCESS")
	}
}
