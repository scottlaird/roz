package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestParseRepoID(t *testing.T) {
	tests := []struct {
		id        string
		wantOwner string
		wantName  string
		wantErr   bool
	}{
		{id: "scottlaird/roz", wantOwner: "scottlaird", wantName: "roz"},
		{id: "a/b", wantOwner: "a", wantName: "b"},
		{id: "", wantErr: true},
		{id: "roz", wantErr: true},
		{id: "/roz", wantErr: true},
		{id: "scottlaird/", wantErr: true},
		{id: "a/b/c", wantErr: true},
		// A pull request key is not a repository.
		{id: "owner/repo#1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			owner, name, err := ParseRepoID(tt.id)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("ParseRepoID(%q) error = %v, want error %v", tt.id, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if owner != tt.wantOwner || name != tt.wantName {
				t.Errorf("ParseRepoID(%q) = %q, %q, want %q, %q",
					tt.id, owner, name, tt.wantOwner, tt.wantName)
			}
			if got := RepoID(owner, name); got != tt.id {
				t.Errorf("RepoID round trip = %q, want %q", got, tt.id)
			}
		})
	}
}

// TestPipelineIsAuthored is the decision that cuts against the usual rule:
// GitHub knows whether review is required, but reading it needs admin on the
// repository, so this is a judgement and sync may not overwrite it.
func TestPipelineIsAuthored(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	r := trackRepo(t, st, "scottlaird/roz")

	sync, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer sync.Rollback()

	after := r.Clone()
	after.Pipeline = sql.NullString{String: PipelineDirect, Valid: true}
	if _, err := sync.Update(ctx, r, after); err == nil {
		t.Error("sync set pipeline, want an error")
	}
}

func TestHumanSetsPipelineAndSyncSetsDefaultBranch(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	r := trackRepo(t, st, "scottlaird/roz")

	human, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	stated := r.Clone()
	stated.Pipeline = sql.NullString{String: PipelineDirect, Valid: true}
	if _, err := human.Update(ctx, r, stated); err != nil {
		t.Fatalf("Update() as human returned error: %v", err)
	}
	if err := human.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	sync, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	observed := stated.Clone()
	observed.DefaultBranch = sql.NullString{String: "main", Valid: true}
	if _, err := sync.Update(ctx, stated, observed); err != nil {
		t.Fatalf("Update() as sync returned error: %v", err)
	}
	if err := sync.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	read, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer read.Rollback()

	loaded, err := read.LoadGitHubRepo(ctx, r.ID)
	if err != nil {
		t.Fatalf("LoadGitHubRepo() returned error: %v", err)
	}
	if loaded.Pipeline.String != PipelineDirect {
		t.Errorf("pipeline = %q, want %q", loaded.Pipeline.String, PipelineDirect)
	}
	if loaded.DefaultBranch.String != "main" {
		t.Errorf("default_branch = %q, want main", loaded.DefaultBranch.String)
	}
}

// TestPRRequiresATrackedRepo is the foreign key doing its job.
func TestPRRequiresATrackedRepo(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.Insert(ctx, NewPR("owner/untracked", 1)); err == nil {
		t.Error("a pull request in an untracked repository was inserted, want a foreign key error")
	}
}

func TestListGitHubRepos(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	trackRepo(t, st, "scottlaird/roz")
	trackRepo(t, st, "anotherowner/thing")
	trackRepo(t, st, "scottlaird/other")

	got, err := st.ListGitHubRepos(ctx)
	if err != nil {
		t.Fatalf("ListGitHubRepos() returned error: %v", err)
	}
	ids := make([]string, len(got))
	for i, r := range got {
		ids[i] = r.ID
	}
	want := []string{"anotherowner/thing", "scottlaird/other", "scottlaird/roz"}
	if !equalStrings(ids, want) {
		t.Errorf("ListGitHubRepos() = %v, want %v", ids, want)
	}
}

func TestLoadSubjectResolvesRepositories(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	r := trackRepo(t, st, "scottlaird/roz")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	subject, err := tx.LoadSubject(ctx, st, r.ID)
	if err != nil {
		t.Fatalf("LoadSubject() returned error: %v", err)
	}
	if subject.subjectType() != "github_repo" || subject.subjectID() != r.ID {
		t.Errorf("LoadSubject() = %s/%s, want github_repo/%s",
			subject.subjectType(), subject.subjectID(), r.ID)
	}
}

func TestLoadGitHubRepoMissing(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.LoadGitHubRepo(ctx, "nobody/nothing"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("LoadGitHubRepo() error = %v, want sql.ErrNoRows", err)
	}
}
