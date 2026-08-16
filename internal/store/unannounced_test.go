package store

import (
	"context"
	"database/sql"
	"testing"
)

// TestAWaitOnAnUnrequestedReviewIsReported is scottlaird/roz#83: an action sat
// as wait_review for four days on a pull request nobody had announced. GitHub
// showed a review request and two teams, so from the outside it looked like an
// ordinary wait — the request was automatic, and roz held the disproof the
// whole time.
func TestAWaitOnAnUnrequestedReviewIsReported(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	_, created := pipelineFor(t, st, "review")

	var wait *Action
	for _, a := range created {
		if a.Verb == "wait_review" {
			wait = a
		}
	}
	if wait == nil {
		t.Fatalf("the fixture has no wait_review action")
	}

	reported, err := st.UnannouncedWaits(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("UnannouncedWaits() returned error: %v", err)
	}
	if len(reported) != 1 || reported[0].Action.ID != wait.ID {
		t.Fatalf("reported %+v, want the one wait nobody announced", reported)
	}

	// Once, not once per cycle: the condition stays true until somebody acts
	// on it, and a monitor that repeats itself is one people turn off.
	again, err := st.UnannouncedWaits(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("UnannouncedWaits() returned error: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("reported %d waits on the second sweep, want none", len(again))
	}
}

// TestAnAnnouncedWaitIsNotReported, which is the ordinary case and has to stay
// silent.
func TestAnAnnouncedWaitIsNotReported(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	pr, _ := pipelineFor(t, st, "review")
	announce(t, st, pr.ID)

	reported, err := st.UnannouncedWaits(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("UnannouncedWaits() returned error: %v", err)
	}
	if len(reported) != 0 {
		t.Errorf("an announced wait was reported: %+v", reported)
	}
}

// TestAPipelineWithoutAnAnnouncementStaysSilent. An empty announced_at is not
// always wrong: a chain with no announcement step never records one, and a
// team relying on GitHub's own notifications has not made a mistake.
func TestAPipelineWithoutAnAnnouncementStaysSilent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// A chain that waits for a review without asking for one first.
	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	quiet := &Pipeline{Name: "quiet", Steps: []PipelineStep{
		{Verb: "wait_review"}, {Verb: "merge"},
	}}
	if err := tx.InsertPipeline(ctx, quiet, ""); err != nil {
		t.Fatalf("InsertPipeline() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	pipelineFor(t, st, "quiet")

	reported, err := st.UnannouncedWaits(ctx, ActorPredicate)
	if err != nil {
		t.Fatalf("UnannouncedWaits() returned error: %v", err)
	}
	if len(reported) != 0 {
		t.Errorf("a chain with no announcement step was reported: %+v", reported)
	}
}

// announce records what `roz pr announce` records, which is the only thing
// this check reads.
func announce(t *testing.T, st *Store, id string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorSlackManual)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	before, err := tx.LoadPR(ctx, id)
	if err != nil {
		t.Fatalf("LoadPR() returned error: %v", err)
	}
	after := before.Clone()
	after.AnnouncedAt = sql.NullString{String: "2026-08-16T09:30:00Z", Valid: true}
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}
