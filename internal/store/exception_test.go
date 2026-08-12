package store

import (
	"context"
	"testing"
	"time"
)

// exceptionsFor counts the exceptions logged for one condition.
func exceptionsFor(t *testing.T, st *Store, kind, subjectID string) int {
	t.Helper()
	n := 0
	for _, e := range events(t, st) {
		if e.Severity == SeverityException && e.Kind == kind && e.SubjectID == subjectID {
			n++
		}
	}
	return n
}

// raiseOnce reports a condition the way a poll does.
func raiseOnce(t *testing.T, st *Store, r Record, kind, note string) bool {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	wrote, err := tx.ExceptionOnce(ctx, r, kind, note)
	if err != nil {
		t.Fatalf("ExceptionOnce() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	return wrote
}

// TestAStandingConditionIsReportedOnce is the whole point: sync re-derives the
// world every few seconds and finds the same unreadable repository each time,
// and the log a monitor watches must not fill up with restatements.
func TestAStandingConditionIsReportedOnce(t *testing.T) {
	st := newStore(t)
	repo := trackRepo(t, st, "acme/api")

	if wrote := raiseOnce(t, st, repo, "ref_poll_truncated", "82234 refs, read 500"); !wrote {
		t.Fatal("the first occurrence was suppressed; it is the one worth having")
	}
	for i := 0; i < 20; i++ {
		if wrote := raiseOnce(t, st, repo, "ref_poll_truncated", "82234 refs, read 500"); wrote {
			t.Fatalf("poll %d reported the same condition again", i+2)
		}
	}

	if got := exceptionsFor(t, st, "ref_poll_truncated", "acme/api"); got != 1 {
		t.Errorf("logged %d exceptions for one standing condition, want 1", got)
	}
}

// TestSuppressionIsPerCondition: a second, unrelated problem on the same
// repository still has to surface. The key is the condition, not the subject.
func TestSuppressionIsPerCondition(t *testing.T) {
	st := newStore(t)
	repo := trackRepo(t, st, "acme/api")
	other := trackRepo(t, st, "acme/web")

	raiseOnce(t, st, repo, "ref_poll_truncated", "too many refs")

	if wrote := raiseOnce(t, st, repo, "repo_unresolvable", "gone private"); !wrote {
		t.Error("a different problem on the same repository was suppressed")
	}
	if wrote := raiseOnce(t, st, other, "ref_poll_truncated", "too many refs"); !wrote {
		t.Error("the same problem on a different repository was suppressed")
	}
}

// TestSuppressionIgnoresTheMessage: the detail moves — a ref count grows — and
// that is not new information worth another line every few seconds. Keying on
// the text would let any rewording defeat the suppression.
func TestSuppressionIgnoresTheMessage(t *testing.T) {
	st := newStore(t)
	repo := trackRepo(t, st, "acme/api")

	raiseOnce(t, st, repo, "ref_poll_truncated", "82234 refs, read 500")
	if wrote := raiseOnce(t, st, repo, "ref_poll_truncated", "82235 refs, read 500"); wrote {
		t.Error("a changed count defeated the suppression")
	}
}

// TestTheConditionIsReportedAgainLater: suppression is a quiet period, not a
// permanent silence. A problem still there tomorrow is worth saying again,
// because the first notice has scrolled out of anybody's view by then.
func TestTheConditionIsReportedAgainLater(t *testing.T) {
	st := newStore(t)
	repo := trackRepo(t, st, "acme/api")

	raiseOnce(t, st, repo, "ref_poll_truncated", "too many refs")
	if wrote := raiseOnce(t, st, repo, "ref_poll_truncated", "too many refs"); wrote {
		t.Fatal("reported twice in a row")
	}

	// Move the clock past the quiet period.
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC).Add(ExceptionInterval + time.Hour)
	var ticks int
	st.now = func() time.Time {
		ticks++
		return base.Add(time.Duration(ticks) * time.Second)
	}

	if wrote := raiseOnce(t, st, repo, "ref_poll_truncated", "too many refs"); !wrote {
		t.Error("a condition still outstanding a day later was never mentioned again")
	}
	if got := exceptionsFor(t, st, "ref_poll_truncated", "acme/api"); got != 2 {
		t.Errorf("logged %d exceptions across two days, want 2", got)
	}
}

// TestExceptionStillWritesUnconditionally: `roz exception` is a person
// deliberately recording one, and must never be swallowed by a rule about
// polling.
func TestExceptionStillWritesUnconditionally(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	repo := trackRepo(t, st, "acme/api")

	for i := 0; i < 3; i++ {
		tx, err := st.Begin(ctx, ActorHuman)
		if err != nil {
			t.Fatalf("Begin() returned error: %v", err)
		}
		if err := tx.Exception(ctx, repo, "looked_wrong", "still looks wrong"); err != nil {
			t.Fatalf("Exception() returned error: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit() returned error: %v", err)
		}
	}

	if got := exceptionsFor(t, st, "looked_wrong", "acme/api"); got != 3 {
		t.Errorf("logged %d hand-written exceptions, want 3", got)
	}
}
