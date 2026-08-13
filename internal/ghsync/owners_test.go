package ghsync

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/scottlaird/roz/internal/github"
	"github.com/scottlaird/roz/internal/store"
)

// changeFetcher is a fakeFetcher that can also answer what a change touches.
type changeFetcher struct {
	*fakeFetcher
	changes map[string]github.Change
	err     error
	asked   []string
}

func (c *changeFetcher) Change(_ context.Context, key string) (github.Change, error) {
	c.asked = append(c.asked, key)
	if c.err != nil {
		return github.Change{}, c.err
	}
	return c.changes[key], nil
}

// observeHead puts a head on a pull request, which is what makes deriving
// possible and what deriving is keyed on.
func observeHead(t *testing.T, st *store.Store, key, head string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, store.ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	before, err := tx.LoadPR(ctx, key)
	if err != nil {
		t.Fatalf("LoadPR() returned error: %v", err)
	}
	after := before.Clone()
	after.HeadSHA = sql.NullString{String: head, Valid: true}
	after.State = sql.NullString{String: store.PRStateOpen, Valid: true}
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

func loadOwners(t *testing.T, st *store.Store, key string) (string, string) {
	t.Helper()
	pr := loadPR(t, st, key)
	head := ""
	if pr.OwnersHead.Valid {
		head = pr.OwnersHead.String
	}
	return pr.RequiredOwners, head
}

// TestOwnersAreDerivedFromTheFiles is the point of the issue: a wait can say
// who it waits for, worked out from what the change touches.
func TestOwnersAreDerivedFromTheFiles(t *testing.T) {
	ctx := context.Background()
	st, key := newStore(t)
	observeHead(t, st, key, "abc123")

	client := &changeFetcher{
		fakeFetcher: &fakeFetcher{},
		changes: map[string]github.Change{key: {
			Key:        key,
			Files:      []string{"storage/db.go", "docs/readme.md"},
			Codeowners: "storage/ @org/storage\ndocs/ @org/docs\n",
		}},
	}

	result, err := Sync(ctx, st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(result.Owners) != 1 {
		t.Fatalf("Sync() reported %d derivations, want 1", len(result.Owners))
	}

	owners, head := loadOwners(t, st, key)
	if want := `["@org/docs","@org/storage"]`; owners != want {
		t.Errorf("required_owners = %s, want %s", owners, want)
	}
	if head != "abc123" {
		t.Errorf("owners_head = %q, want the head it was worked out against", head)
	}
}

// TestOwnersAreNotRederivedWhileTheHeadStands: the read is a paginated file
// list plus a CODEOWNERS fetch per pull request, and the answer cannot change
// while the pull request does not.
func TestOwnersAreNotRederivedWhileTheHeadStands(t *testing.T) {
	ctx := context.Background()
	st, key := newStore(t)
	observeHead(t, st, key, "abc123")

	client := &changeFetcher{
		fakeFetcher: &fakeFetcher{},
		changes: map[string]github.Change{key: {
			Key: key, Files: []string{"storage/db.go"},
			Codeowners: "storage/ @org/storage\n",
		}},
	}

	for i := 0; i < 4; i++ {
		if _, err := Sync(ctx, st, client); err != nil {
			t.Fatalf("Sync() returned error: %v", err)
		}
	}
	if len(client.asked) != 1 {
		t.Errorf("read the change %d times for one head, want 1", len(client.asked))
	}

	// The head moves, and the answer is worked out again.
	observeHead(t, st, key, "def456")
	client.changes[key] = github.Change{
		Key: key, Files: []string{"storage/db.go", "billing/rates.go"},
		Codeowners: "storage/ @org/storage\nbilling/ @org/billing\n",
	}
	result, err := Sync(ctx, st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(client.asked) != 2 {
		t.Errorf("read the change %d times after the head moved, want 2", len(client.asked))
	}
	if len(result.Owners) != 1 {
		t.Fatalf("the change of owners was not reported")
	}
	owners, _ := loadOwners(t, st, key)
	if want := `["@org/billing","@org/storage"]`; owners != want {
		t.Errorf("required_owners = %s, want %s", owners, want)
	}
}

// TestOwnersUnchangedIsNotNews: a head that moves without changing who is
// needed is checked, not reported. Most pushes are like this.
func TestOwnersUnchangedIsNotNews(t *testing.T) {
	ctx := context.Background()
	st, key := newStore(t)
	observeHead(t, st, key, "abc123")

	client := &changeFetcher{
		fakeFetcher: &fakeFetcher{},
		changes: map[string]github.Change{key: {
			Key: key, Files: []string{"storage/db.go"},
			Codeowners: "storage/ @org/storage\n",
		}},
	}
	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}

	observeHead(t, st, key, "def456")
	result, err := Sync(ctx, st, client)
	if err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(result.Owners) != 0 {
		t.Errorf("reported %v for a push that changed nobody", result.Owners)
	}
	// It was still checked, so it will not be checked again.
	if _, head := loadOwners(t, st, key); head != "def456" {
		t.Errorf("owners_head = %q, want the new head", head)
	}
}

// TestARepositoryWithNoCodeowners answers "nobody" rather than failing, which
// is the ordinary case for most repositories.
func TestARepositoryWithNoCodeowners(t *testing.T) {
	ctx := context.Background()
	st, key := newStore(t)
	observeHead(t, st, key, "abc123")

	client := &changeFetcher{
		fakeFetcher: &fakeFetcher{},
		changes:     map[string]github.Change{key: {Key: key, Files: []string{"main.go"}}},
	}
	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}

	owners, head := loadOwners(t, st, key)
	if owners != "[]" {
		t.Errorf("required_owners = %s, want []", owners)
	}
	// The head is what says this was worked out, rather than never attempted.
	if head != "abc123" {
		t.Errorf("owners_head = %q, want it recorded", head)
	}
}

// TestAnUnreadableChangeDoesNotStopTheSync: one repository whose files cannot
// be read must not take the poll down with it.
func TestAnUnreadableChangeDoesNotStopTheSync(t *testing.T) {
	ctx := context.Background()
	st, key := newStore(t)
	observeHead(t, st, key, "abc123")

	client := &changeFetcher{fakeFetcher: &fakeFetcher{}, err: fmt.Errorf("boom")}
	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}

	events, err := st.Events(ctx, store.EventQuery{Severity: store.SeverityException})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	var reported int
	for _, e := range events {
		if e.Kind == eventOwnersUnreadable {
			reported++
		}
	}
	if reported != 1 {
		t.Errorf("logged %d exceptions for an unreadable change, want 1", reported)
	}
}

// TestAPullRequestWithNoHeadIsNotDerived: nothing has observed what it points
// at, so there is nothing to match files against yet.
func TestAPullRequestWithNoHeadIsNotDerived(t *testing.T) {
	ctx := context.Background()
	st, _ := newStore(t)

	client := &changeFetcher{fakeFetcher: &fakeFetcher{}}
	if _, err := Sync(ctx, st, client); err != nil {
		t.Fatalf("Sync() returned error: %v", err)
	}
	if len(client.asked) != 0 {
		t.Errorf("read the change for a pull request with no head: %v", client.asked)
	}
}
