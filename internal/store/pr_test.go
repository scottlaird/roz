package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestParsePRKey(t *testing.T) {
	tests := []struct {
		key        string
		wantRepo   string
		wantNumber int64
		wantErr    bool
	}{
		{key: "owner/myrepo#812", wantRepo: "owner/myrepo", wantNumber: 812},
		{key: "scottlaird/roz#11", wantRepo: "scottlaird/roz", wantNumber: 11},
		{key: "", wantErr: true},
		// A bare repository name is no longer enough: pr.repo is a foreign key
		// into github_repo, which is keyed owner/name.
		{key: "saas-infra-plane#4174", wantErr: true},
		{key: "myrepo#812", wantErr: true},
		{key: "owner/repo", wantErr: true},
		{key: "#812", wantErr: true},
		{key: "owner/repo#", wantErr: true},
		{key: "owner/repo#abc", wantErr: true},
		{key: "owner/repo#0", wantErr: true},
		{key: "owner/repo#-1", wantErr: true},
		// A second separator would make the key not round-trip through the
		// schema's CHECK (id = repo || '#' || number).
		{key: "owner/my#repo#812", wantErr: true},
		{key: "a/b/c#1", wantErr: true},
		// A sequence identifier is not a pull request key.
		{key: "SL200", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			repo, number, err := ParsePRKey(tt.key)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("ParsePRKey(%q) error = %v, want error %v", tt.key, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if repo != tt.wantRepo || number != tt.wantNumber {
				t.Errorf("ParsePRKey(%q) = %q, %d, want %q, %d",
					tt.key, repo, number, tt.wantRepo, tt.wantNumber)
			}
			if got := PRKey(repo, number); got != tt.key {
				t.Errorf("PRKey round trip = %q, want %q", got, tt.key)
			}
		})
	}
}

// trackRepo records a repository, which a pull request in it needs first.
func trackRepo(t *testing.T, st *Store, id string) *GitHubRepo {
	t.Helper()
	ctx := context.Background()

	owner, name, err := ParseRepoID(id)
	if err != nil {
		t.Fatalf("ParseRepoID(%q) returned error: %v", id, err)
	}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if existing, err := tx.LoadGitHubRepo(ctx, id); err == nil {
		return existing
	}

	r := NewGitHubRepo(owner, name)
	if err := tx.Insert(ctx, r); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	return r
}

// trackPR records and commits a tracking decision, tracking the repository
// first because pr.repo is a foreign key into github_repo.
func trackPR(t *testing.T, st *Store, repo string, number int64) *PR {
	t.Helper()
	ctx := context.Background()

	trackRepo(t, st, repo)

	p := NewPR(repo, number)
	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.Insert(ctx, p); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	return p
}

// TestHumanMayTrackAPR is the case the insert rule has to allow: tracking is
// the one judgement a pull request carries, and the empty JSON containers a
// new one holds are not observations.
func TestHumanMayTrackAPR(t *testing.T) {
	st := newStore(t)
	p := trackPR(t, st, "owner/myrepo", 812)

	if p.ID != "owner/myrepo#812" {
		t.Errorf("id = %q, want owner/myrepo#812", p.ID)
	}
	if p.TrackedSince == "" {
		t.Error("tracked_since is empty after Insert()")
	}
}

func TestPRRoundTrips(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := trackPR(t, st, "owner/myrepo", 812)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	loaded, err := tx.LoadPR(ctx, p.ID)
	if err != nil {
		t.Fatalf("LoadPR() returned error: %v", err)
	}
	if *loaded != *p {
		t.Errorf("loaded = %+v, want %+v", *loaded, *p)
	}
}

func TestHumanMayNotWriteObservedPRColumns(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := trackPR(t, st, "owner/myrepo", 812)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := p.Clone()
	after.State = sql.NullString{String: PRStateMerged, Valid: true}

	if _, err := tx.Update(ctx, p, after); err == nil {
		t.Error("a human set pr.state, want an error")
	}
}

// TestSyncWritesObservedPRColumns is the mirror, and the first place the
// observed half of the rule is exercised for real.
func TestSyncWritesObservedPRColumns(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := trackPR(t, st, "owner/myrepo", 812)

	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := p.Clone()
	after.State = sql.NullString{String: PRStateOpen, Valid: true}
	after.IsDraft = sql.NullBool{Bool: true, Valid: true}
	after.ReviewDecision = sql.NullString{String: "REVIEW_REQUIRED", Valid: true}

	changes, err := tx.Update(ctx, p, after)
	if err != nil {
		t.Fatalf("Update() as sync returned error: %v", err)
	}
	if len(changes) != 3 {
		t.Errorf("got %d changes, want 3", len(changes))
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// TestFrozenIsComputedByTheDatabase covers the one genuinely derived column:
// it is never written, and it follows from the two freeze signals.
func TestFrozenIsComputedByTheDatabase(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := trackPR(t, st, "owner/myrepo", 812)

	if p.Frozen {
		t.Error("a newly tracked pull request is frozen, want not frozen")
	}

	tx, err := st.Begin(ctx, ActorSyncSlack)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	after := p.Clone()
	after.AnnouncedAt = sql.NullString{String: "2026-08-09T12:00:00.000Z", Valid: true}
	if _, err := tx.Update(ctx, p, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	read, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer read.Rollback()

	loaded, err := read.LoadPR(ctx, p.ID)
	if err != nil {
		t.Fatalf("LoadPR() returned error: %v", err)
	}
	if !loaded.Frozen {
		t.Error("announcing did not freeze the pull request")
	}
}

// TestSettingFrozenIsNotAChange checks the derived column cannot be written
// even by accident: a stale in-memory value is ignored rather than attempted.
func TestSettingFrozenIsNotAChange(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := trackPR(t, st, "owner/myrepo", 812)

	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := p.Clone()
	after.Frozen = true

	changes, err := tx.Update(ctx, p, after)
	if err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("setting frozen produced %v, want no changes", changes)
	}
}

func TestListPRs(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	base := trackPR(t, st, "owner/myrepo", 100)
	stacked := trackPR(t, st, "owner/myrepo", 101)
	trackPR(t, st, "owner/otherrepo", 5)

	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	after := stacked.Clone()
	after.StackedOn = sql.NullString{String: base.ID, Valid: true}
	after.State = sql.NullString{String: PRStateOpen, Valid: true}
	if _, err := tx.Update(ctx, stacked, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	tests := []struct {
		name   string
		filter PRFilter
		want   []string
	}{
		{name: "all, ordered by repo then number", filter: PRFilter{},
			want: []string{"owner/myrepo#100", "owner/myrepo#101", "owner/otherrepo#5"}},
		{name: "stacked", filter: PRFilter{Stacked: true}, want: []string{"owner/myrepo#101"}},
		{name: "by state", filter: PRFilter{State: PRStateOpen}, want: []string{"owner/myrepo#101"}},
		{name: "frozen", filter: PRFilter{Frozen: true}, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := st.ListPRs(ctx, tt.filter)
			if err != nil {
				t.Fatalf("ListPRs() returned error: %v", err)
			}
			ids := make([]string, len(got))
			for i, p := range got {
				ids[i] = p.ID
			}
			if !equalStrings(ids, tt.want) {
				t.Errorf("ListPRs(%+v) = %v, want %v", tt.filter, ids, tt.want)
			}
		})
	}
}

func TestLoadSubjectResolvesPRKeys(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := trackPR(t, st, "owner/myrepo", 812)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	subject, err := tx.LoadSubject(ctx, st, p.ID)
	if err != nil {
		t.Fatalf("LoadSubject() returned error: %v", err)
	}
	if subject.subjectType() != "pr" || subject.subjectID() != p.ID {
		t.Errorf("LoadSubject() = %s/%s, want pr/%s",
			subject.subjectType(), subject.subjectID(), p.ID)
	}
}

// TestTrackedBecauseIsAuthored: it is a judgement about why you are tracking
// something, not a fact GitHub reported, so sync must not be able to write it.
func TestTrackedBecauseIsAuthored(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	trackRepo(t, st, "scottlaird/roz")
	pr := trackPR(t, st, "scottlaird/roz", 1)

	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := pr.Clone()
	after.TrackedBecause = sql.NullString{String: TrackedReviewing, Valid: true}
	if _, err := tx.Update(ctx, pr, after); err == nil {
		t.Error("sync wrote tracked_because, want the authored rule to refuse it")
	}
}

// TestTheSchemaRefusesAnUnknownReason: the CHECK is the backstop under the
// command's validation, so a direct write cannot invent a fourth reason.
func TestTheSchemaRefusesAnUnknownReason(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	trackRepo(t, st, "scottlaird/roz")
	pr := trackPR(t, st, "scottlaird/roz", 1)

	_, err := st.db.ExecContext(ctx,
		"UPDATE pr SET tracked_because = 'curious' WHERE id = ?", pr.ID)
	if err == nil {
		t.Error("the schema accepted a reason outside the set")
	}
}

// TestListPRsSince is the week in review: what merged, in the order it merged.
//
// Ordered by merged_at rather than by repository, because a week is read
// chronologically and grouping it by repository answers a different question.
func TestListPRsSince(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	// Deliberately out of merge order by identifier, so an accidental
	// ORDER BY repo, number would still look right.
	mergePR(t, st, trackPR(t, st, "owner/zeta", 1), "2026-08-05T09:00:00.000Z")
	mergePR(t, st, trackPR(t, st, "owner/alpha", 9), "2026-08-07T09:00:00.000Z")
	mergePR(t, st, trackPR(t, st, "owner/beta", 4), "2026-08-06T09:00:00.000Z")
	// One that never merged, and one merged before merged_at was recorded.
	trackPR(t, st, "owner/gamma", 2)
	openPR := trackPR(t, st, "owner/delta", 3)
	setState(t, st, openPR, PRStateMerged)

	tests := []struct {
		name   string
		filter PRFilter
		want   []string
	}{
		{
			name:   "a window, oldest merge first",
			filter: PRFilter{Since: "2026-08-06"},
			want:   []string{"owner/beta#4", "owner/alpha#9"},
		},
		{
			// Since reads merged_at, so it selects merged pull requests on its
			// own — an open one has not finished, whatever else is asked.
			name:   "an open pull request is never in a window",
			filter: PRFilter{Since: "2000-01-01"},
			want:   []string{"owner/zeta#1", "owner/beta#4", "owner/alpha#9"},
		},
		{
			// A merge with no time goes last rather than first, which is
			// where SQLite puts NULL on its own.
			name:   "state alone takes the same order, undated last",
			filter: PRFilter{State: PRStateMerged},
			want: []string{
				"owner/zeta#1", "owner/beta#4", "owner/alpha#9", "owner/delta#3",
			},
		},
		{
			name:   "no window keeps the repository order",
			filter: PRFilter{},
			want: []string{
				"owner/alpha#9", "owner/beta#4", "owner/delta#3",
				"owner/gamma#2", "owner/zeta#1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := st.ListPRs(ctx, tt.filter)
			if err != nil {
				t.Fatalf("ListPRs() returned error: %v", err)
			}
			ids := make([]string, len(got))
			for i, p := range got {
				ids[i] = p.ID
			}
			if !equalStrings(ids, tt.want) {
				t.Errorf("ListPRs(%+v) = %v, want %v", tt.filter, ids, tt.want)
			}
		})
	}
}

// mergePR records a merge the way sync would.
func mergePR(t *testing.T, st *Store, p *PR, at string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := p.Clone()
	after.State = sql.NullString{String: PRStateMerged, Valid: true}
	after.MergedAt = sql.NullString{String: at, Valid: true}
	if _, err := tx.Update(ctx, p, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// setState moves a pull request without saying when, which is what a merge
// recorded before merged_at existed looks like.
func setState(t *testing.T, st *Store, p *PR, state string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := p.Clone()
	after.State = sql.NullString{String: state, Valid: true}
	if _, err := tx.Update(ctx, p, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// TestPRsToPoll is scottlaird/roz#174: a pull request that ended months ago
// was re-read on every cycle for ever, and nothing untracks a row, so the set
// only grew.
func TestPRsToPoll(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	const now = "2026-08-15T00:00:00.000Z"

	open := trackPR(t, st, "owner/repo", 1)
	setState(t, st, open, PRStateOpen)

	old := trackPR(t, st, "owner/repo", 2)
	endPR(t, st, old, PRStateMerged, "2026-01-01T00:00:00.000Z")

	recent := trackPR(t, st, "owner/repo", 3)
	endPR(t, st, recent, PRStateMerged, "2026-08-14T00:00:00.000Z")

	// Ended long ago, and something is still being done about it.
	worked := trackPR(t, st, "owner/repo", 4)
	endPR(t, st, worked, PRStateMerged, "2026-01-01T00:00:00.000Z")
	a := addAction(t, st, "follow up on the merge", "investigate")
	linkActionPR(t, st, a, worked.ID)

	// Terminal with no date: a row recorded before closed_at existed. One
	// more read gives it a date the next window can drop it on, so it is in.
	undated := trackPR(t, st, "owner/repo", 5)
	setState(t, st, undated, PRStateClosed)

	got, err := st.PRsToPoll(ctx, PollWindow{Days: 14, Now: now})
	if err != nil {
		t.Fatalf("PRsToPoll() returned error: %v", err)
	}
	want := []string{
		"owner/repo#1", // open
		"owner/repo#3", // ended inside the window
		"owner/repo#4", // ended outside it, but an action is about it
		"owner/repo#5", // ended, undated
	}
	if !equalStrings(got, want) {
		t.Errorf("PRsToPoll() = %v, want %v", got, want)
	}

	// A window of nothing still keeps what is open, and what is worked on.
	got, err = st.PRsToPoll(ctx, PollWindow{Days: 0, Now: now})
	if err != nil {
		t.Fatalf("PRsToPoll() returned error: %v", err)
	}
	for _, key := range []string{"owner/repo#1", "owner/repo#4"} {
		if !contains(got, key) {
			t.Errorf("a zero window dropped %s: %v", key, got)
		}
	}
	if contains(got, "owner/repo#3") {
		t.Errorf("a zero window kept a merged pull request: %v", got)
	}
}

// TestAnOpenPullRequestIsPolledHoweverOld is the caveat that would be a bug
// backwards: the transition into MERGED is itself an observation, so a row
// that is locally open has to stay in the poll set whatever its age. Filtering
// on what GitHub last said instead would mean a pull request that merges is
// never seen to have merged.
func TestAnOpenPullRequestIsPolledHoweverOld(t *testing.T) {
	st := newStore(t)

	ancient := trackPR(t, st, "owner/repo", 1)
	setState(t, st, ancient, PRStateOpen)

	got, err := st.PRsToPoll(context.Background(),
		PollWindow{Days: 1, Now: "2030-01-01T00:00:00.000Z"})
	if err != nil {
		t.Fatalf("PRsToPoll() returned error: %v", err)
	}
	if !contains(got, ancient.ID) {
		t.Errorf("an open pull request aged out of the poll set: %v", got)
	}
}

// linkActionPR records that an action is about a pull request.
func linkActionPR(t *testing.T, st *Store, a *Action, prID string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()
	if err := tx.LinkPR(ctx, a, prID, RoleSubject); err != nil {
		t.Fatalf("LinkPR() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// endPR records an ending the way sync would.
func endPR(t *testing.T, st *Store, p *PR, state, at string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := p.Clone()
	after.State = sql.NullString{String: state, Valid: true}
	after.ClosedAt = sql.NullString{String: at, Valid: true}
	if state == PRStateMerged {
		after.MergedAt = sql.NullString{String: at, Valid: true}
	}
	if _, err := tx.Update(ctx, p, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// TestResolveStacking is scottlaird/roz#172: the column existed, `pr list
// --stacked` filtered on it, and nothing ever wrote it — because the head
// branch was not stored, so there was nothing to match a base against.
func TestResolveStacking(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// A chain of three, and one unrelated.
	base := trackPR(t, st, "owner/repo", 123)
	setBranches(t, st, base, "main", "feature-a")
	middle := trackPR(t, st, "owner/repo", 124)
	setBranches(t, st, middle, "feature-a", "feature-b")
	top := trackPR(t, st, "owner/repo", 125)
	setBranches(t, st, top, "feature-b", "feature-c")
	alone := trackPR(t, st, "owner/repo", 200)
	setBranches(t, st, alone, "main", "unrelated")

	if _, err := st.ResolveStacking(ctx, ActorSyncGitHub); err != nil {
		t.Fatalf("ResolveStacking() returned error: %v", err)
	}

	// Each link resolves, rather than only the first.
	for _, tt := range []struct{ id, want string }{
		{middle.ID, base.ID},
		{top.ID, middle.ID},
	} {
		if got := loadPRRow(t, st, tt.id).StackedOn.String; got != tt.want {
			t.Errorf("%s stacked_on = %q, want %q", tt.id, got, tt.want)
		}
	}
	// Based on the default branch, so on nothing tracked.
	for _, id := range []string{base.ID, alone.ID} {
		if got := loadPRRow(t, st, id).StackedOn; got.Valid {
			t.Errorf("%s stacked_on = %q, want nothing", id, got.String)
		}
	}

	listed, err := st.ListPRs(ctx, PRFilter{Stacked: true})
	if err != nil {
		t.Fatalf("ListPRs() returned error: %v", err)
	}
	if len(listed) != 2 {
		t.Errorf("--stacked returned %d, want the two with a parent", len(listed))
	}
}

// TestStackingIsReDerivedRatherThanLatched: a rebase onto the default branch
// clears it, with nothing having to notice the relationship ended.
func TestStackingIsReDerivedRatherThanLatched(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	parent := trackPR(t, st, "owner/repo", 123)
	setBranches(t, st, parent, "main", "feature-a")
	child := trackPR(t, st, "owner/repo", 124)
	setBranches(t, st, child, "feature-a", "feature-b")

	if _, err := st.ResolveStacking(ctx, ActorSyncGitHub); err != nil {
		t.Fatalf("ResolveStacking() returned error: %v", err)
	}
	if got := loadPRRow(t, st, child.ID).StackedOn.String; got != parent.ID {
		t.Fatalf("stacked_on = %q, want %q", got, parent.ID)
	}

	// Rebased onto the default branch.
	setBranches(t, st, loadPRRow(t, st, child.ID), "main", "feature-b")
	changed, err := st.ResolveStacking(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("ResolveStacking() returned error: %v", err)
	}
	if got := loadPRRow(t, st, child.ID).StackedOn; got.Valid {
		t.Errorf("stacked_on = %q after a rebase, want it cleared", got.String)
	}
	if len(changed) != 1 {
		t.Errorf("the change was reported as %v, want the one that moved", changed)
	}

	// And a run that changes nothing writes nothing, so a quiet poll stays
	// quiet in the log.
	changed, err = st.ResolveStacking(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("ResolveStacking() returned error: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("a settled run reported %v", changed)
	}
}

// TestStackingIgnoresWhatIsNotTracked: basing on a branch belonging to
// somebody else's pull request, or to none, leaves the column empty rather
// than guessing.
func TestStackingIgnoresWhatIsNotTracked(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// Same branch names, different repositories: a base only matches a head
	// in the repository it belongs to.
	trackRepo(t, st, "owner/other")
	elsewhere := trackPR(t, st, "owner/other", 1)
	setBranches(t, st, elsewhere, "main", "feature-a")
	child := trackPR(t, st, "owner/repo", 124)
	setBranches(t, st, child, "feature-a", "feature-b")

	if _, err := st.ResolveStacking(ctx, ActorSyncGitHub); err != nil {
		t.Fatalf("ResolveStacking() returned error: %v", err)
	}
	if got := loadPRRow(t, st, child.ID).StackedOn; got.Valid {
		t.Errorf("stacked_on = %q, want nothing across repositories", got.String)
	}
}

// TestStackingDoesNotLoopOnACycle: nothing walks the chain, so a cycle GitHub
// should never report cannot hang the resolver.
func TestStackingDoesNotLoopOnACycle(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	one := trackPR(t, st, "owner/repo", 1)
	setBranches(t, st, one, "b", "a")
	two := trackPR(t, st, "owner/repo", 2)
	setBranches(t, st, two, "a", "b")

	if _, err := st.ResolveStacking(ctx, ActorSyncGitHub); err != nil {
		t.Fatalf("ResolveStacking() returned error: %v", err)
	}
	if got := loadPRRow(t, st, one.ID).StackedOn.String; got != two.ID {
		t.Errorf("%s stacked_on = %q", one.ID, got)
	}
	if got := loadPRRow(t, st, two.ID).StackedOn.String; got != one.ID {
		t.Errorf("%s stacked_on = %q", two.ID, got)
	}
}

// setBranches records what a pull request targets and what it is from.
func setBranches(t *testing.T, st *Store, p *PR, base, head string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := p.Clone()
	after.BaseRef = sql.NullString{String: base, Valid: true}
	after.HeadRef = sql.NullString{String: head, Valid: true}
	if _, err := tx.Update(ctx, p, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// loadPRRow reads one back.
func loadPRRow(t *testing.T, st *Store, id string) *PR {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	p, err := tx.LoadPR(ctx, id)
	if err != nil {
		t.Fatalf("LoadPR(%s) returned error: %v", id, err)
	}
	return p
}
