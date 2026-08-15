package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// approveBy records who has approved, as sync would.
func approveBy(t *testing.T, st *Store, key string, logins ...string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	before, err := tx.LoadPR(ctx, key)
	if err != nil {
		t.Fatalf("LoadPR() returned error: %v", err)
	}
	after := before.Clone()
	after.Approvals = `["` + strings.Join(logins, `","`) + `"]`
	if len(logins) == 0 {
		after.Approvals = "[]"
	}
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

func recordMembers(t *testing.T, st *Store, team string, logins ...string) {
	t.Helper()
	if err := st.RecordTeamMembers(context.Background(), ActorSyncGitHub,
		map[string][]string{team: logins}); err != nil {
		t.Fatalf("RecordTeamMembers() returned error: %v", err)
	}
}

// reviewStep creates an open wait_review_from against a pull request.
func reviewStep(t *testing.T, st *Store, prID, owner string) *Action {
	t.Helper()
	ctx := context.Background()

	a := NewAction("wait for review from "+owner, "wait_review_from")
	a.WaitingFor = sql.NullString{String: owner, Valid: true}
	if err := st.AllocateAction(ctx, a); err != nil {
		t.Fatalf("AllocateAction() returned error: %v", err)
	}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.Insert(ctx, a); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}
	if err := tx.LinkPR(ctx, a, prID, RoleSubject); err != nil {
		t.Fatalf("LinkPR() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	return a
}

func settleNow(t *testing.T, st *Store) []Settled {
	t.Helper()
	settled, err := st.Settle(context.Background(), ActorPredicate)
	if err != nil {
		t.Fatalf("Settle() returned error: %v", err)
	}
	return settled
}

// TestAMemberApprovingSatisfiesTheirGroup is the feature: a step waits for one
// group, and an approval arrives as a person.
func TestAMemberApprovingSatisfiesTheirGroup(t *testing.T) {
	st := newStore(t)
	pr := trackPR(t, st, "owner/repo", 1)
	recordMembers(t, st, "@org/storage", "@alice", "@bob")
	step := reviewStep(t, st, pr.ID, "@org/storage")

	approveBy(t, st, pr.ID, "bob")

	settled := settleNow(t, st)
	if len(settled) != 1 || settled[0].Action.ID != step.ID {
		t.Fatalf("settled %d actions, want %s closed", len(settled), step.ID)
	}
}

// TestAnotherGroupsApprovalIsNotThisGroups: the whole point. reviewDecision
// says APPROVED once for the pull request as a whole, which for a change
// needing three reviews in order is the wrong answer twice.
func TestAnotherGroupsApprovalIsNotThisGroups(t *testing.T) {
	st := newStore(t)
	pr := trackPR(t, st, "owner/repo", 1)
	recordMembers(t, st, "@org/storage", "@alice")
	recordMembers(t, st, "@org/api", "@carol")
	reviewStep(t, st, pr.ID, "@org/storage")

	approveBy(t, st, pr.ID, "carol")

	if settled := settleNow(t, st); len(settled) != 0 {
		t.Errorf("an approval from another group closed the step: %d closed", len(settled))
	}
}

// TestAGroupNobodyHasReadIsNotApproved: absence is not completion. A wait that
// stays open because membership was never read is visible in the queue; one
// that closed for that reason would not be.
func TestAGroupNobodyHasReadIsNotApproved(t *testing.T) {
	st := newStore(t)
	pr := trackPR(t, st, "owner/repo", 1)
	reviewStep(t, st, pr.ID, "@org/storage")

	approveBy(t, st, pr.ID, "alice")

	if settled := settleNow(t, st); len(settled) != 0 {
		t.Errorf("an unread group counted as approved: %d closed", len(settled))
	}

	// And it closes as soon as the membership arrives, without the pull
	// request changing at all.
	recordMembers(t, st, "@org/storage", "@alice")
	if settled := settleNow(t, st); len(settled) != 1 {
		t.Errorf("reading the membership did not settle it: %d closed", len(settled))
	}
}

// TestAGroupMayBeAPerson: a tier that resolves to one individual is a real
// case, and their own approval is the answer. Spelling is compared the way
// CODEOWNERS compares — no @, case-insensitively — because the same person is
// "alice" in an approval and "@Alice" in whatever somebody typed.
func TestAGroupMayBeAPerson(t *testing.T) {
	st := newStore(t)
	pr := trackPR(t, st, "owner/repo", 1)
	reviewStep(t, st, pr.ID, "@Alice")

	approveBy(t, st, pr.ID, "alice")

	if settled := settleNow(t, st); len(settled) != 1 {
		t.Errorf("a person's own approval did not close their step: %d closed", len(settled))
	}
}

// TestTeamsAwaitedIsBoundedByWhatIsWaiting: the membership read is one request
// per organisation, and what makes that affordable is asking only about the
// groups an open step names.
func TestTeamsAwaitedIsBoundedByWhatIsWaiting(t *testing.T) {
	st := newStore(t)
	pr := trackPR(t, st, "owner/repo", 1)
	step := reviewStep(t, st, pr.ID, "@org/storage")

	// A note about who a plain wait is for is not a step waiting on a group,
	// and must not put a team into the poll.
	waitFor(t, st, pr.ID, "@org/api")

	awaited, err := st.TeamsAwaited(context.Background())
	if err != nil {
		t.Fatalf("TeamsAwaited() returned error: %v", err)
	}
	if len(awaited) != 1 || awaited[0] != "@org/storage" {
		t.Fatalf("TeamsAwaited() = %v, want only the group a step waits for", awaited)
	}

	// And a closed step stops being a reason to read anything.
	if _, err := st.CloseAction(context.Background(), ActorHuman,
		CloseRequest{ID: step.ID, Reason: ClosedDropped}); err != nil {
		t.Fatalf("CloseAction() returned error: %v", err)
	}
	if awaited, err = st.TeamsAwaited(context.Background()); err != nil {
		t.Fatalf("TeamsAwaited() returned error: %v", err)
	}
	if len(awaited) != 0 {
		t.Errorf("TeamsAwaited() = %v after the step closed, want none", awaited)
	}
}

// TestAnEmptyGroupIsRecordedAsRead: an empty team is a real answer — the
// members left, or the token cannot see them — and treating it as unread would
// spend a request on it every cycle.
func TestAnEmptyGroupIsRecordedAsRead(t *testing.T) {
	st := newStore(t)
	recordMembers(t, st, "@org/storage")

	ages, err := st.TeamMembershipAge(context.Background())
	if err != nil {
		t.Fatalf("TeamMembershipAge() returned error: %v", err)
	}
	if _, ok := ages["@org/storage"]; !ok {
		t.Errorf("an empty group has no read time: %v", ages)
	}
}

// TestAStepMustSayWhoseReviewItWaitsFor refuses the mistake where it is made.
// A step written without a group would instantiate a wait that can never
// close, blocking everything behind it.
func TestAStepMustSayWhoseReviewItWaitsFor(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	tests := []struct {
		name string
		step PipelineStep
		want string
	}{
		{
			name: "no group",
			step: PipelineStep{Verb: "wait_review_from"},
			want: "has to say whose",
		},
		{
			name: "a list",
			step: PipelineStep{Verb: "wait_review_from", Spec: "@org/storage,@org/api"},
			want: "more than one owner",
		},
		{
			name: "not an owner",
			step: PipelineStep{Verb: "wait_review_from", Spec: ">=minor+1"},
			want: "is not an owner",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tx.CheckPipelineSteps(ctx, []PipelineStep{tc.step})
			if err == nil {
				t.Fatalf("CheckPipelineSteps accepted %+v", tc.step)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
