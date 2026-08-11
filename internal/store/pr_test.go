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
		{key: "scottlaird/todo#11", wantRepo: "scottlaird/todo", wantNumber: 11},
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

	trackRepo(t, st, "scottlaird/todo")
	pr := trackPR(t, st, "scottlaird/todo", 1)

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

	trackRepo(t, st, "scottlaird/todo")
	pr := trackPR(t, st, "scottlaird/todo", 1)

	_, err := st.db.ExecContext(ctx,
		"UPDATE pr SET tracked_because = 'curious' WHERE id = ?", pr.ID)
	if err == nil {
		t.Error("the schema accepted a reason outside the set")
	}
}
