package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// observeRequiredOwners writes what sync would have derived from the files.
func observeRequiredOwners(t *testing.T, st *Store, key, owners string) {
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
	after.RequiredOwners = owners
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

func setChannel(t *testing.T, st *Store, owner, channel string) {
	t.Helper()
	if _, err := st.SetOwnerChannel(context.Background(), ActorHuman, owner, channel); err != nil {
		t.Fatalf("SetOwnerChannel(%q) returned error: %v", owner, err)
	}
}

func channelFor(t *testing.T, st *Store, key string) (*AnnounceTarget, []string) {
	t.Helper()
	target, considered, err := st.ChannelFor(context.Background(), key)
	if err != nil {
		t.Fatalf("ChannelFor(%q) returned error: %v", key, err)
	}
	return target, considered
}

// TestAPersonHasNoChannel: a channel is how you reach a group, and an
// individual is reached by naming them. Saying so beats inventing one.
func TestAPersonHasNoChannel(t *testing.T) {
	st := newStore(t)

	_, err := st.SetOwnerChannel(context.Background(), ActorHuman, "@alice", "#alice")
	if err == nil {
		t.Fatal("SetOwnerChannel accepted a person")
	}
	if !strings.Contains(err.Error(), "person") {
		t.Errorf("error does not say why: %v", err)
	}
}

// TestThePreferredOwnerIsAskedFirst: the repository's preference orders the
// owners the change requires, exactly as routing orders them.
func TestThePreferredOwnerIsAskedFirst(t *testing.T) {
	st := newStore(t)
	pr := trackPR(t, st, "owner/repo", 1)
	observeRequiredOwners(t, st, pr.ID, `["@org/api","@org/storage"]`)
	setChannel(t, st, "@org/api", "#api-reviews")
	setChannel(t, st, "@org/storage", "#storage-reviews")

	// Alphabetically @org/api comes first, so a preference that changes the
	// answer is what proves the preference is read at all.
	setHints(t, st, "owner/repo", []string{"@org/storage"})

	target, _ := channelFor(t, st, pr.ID)
	if target == nil {
		t.Fatal("ChannelFor found nothing")
	}
	if target.Channel != "#storage-reviews" {
		t.Errorf("channel = %q, want #storage-reviews", target.Channel)
	}
	if target.Why != ChannelFromHint {
		t.Errorf("why = %q, want %q", target.Why, ChannelFromHint)
	}
}

// TestAPreferenceThisChangeDoesNotNeedIsSkipped: a hint is a preference, not
// an assertion. One that owns nothing here would be announcing to a team with
// no stake in the change.
func TestAPreferenceThisChangeDoesNotNeedIsSkipped(t *testing.T) {
	st := newStore(t)
	pr := trackPR(t, st, "owner/repo", 1)
	observeRequiredOwners(t, st, pr.ID, `["@org/storage"]`)
	setChannel(t, st, "@org/platform", "#platform-reviews")
	setChannel(t, st, "@org/storage", "#storage-reviews")
	setHints(t, st, "owner/repo", []string{"@org/platform"})

	target, _ := channelFor(t, st, pr.ID)
	if target == nil {
		t.Fatal("ChannelFor found nothing")
	}
	if target.Channel != "#storage-reviews" {
		t.Errorf("channel = %q, want #storage-reviews", target.Channel)
	}
	if target.Why != ChannelFromRequired {
		t.Errorf("why = %q, want %q", target.Why, ChannelFromRequired)
	}
}

// TestTheOwnerBeingWaitedForWins: where somebody has said which owner this is
// waiting for, that is the owner actually asked, and reading CODEOWNERS again
// does not improve on it.
func TestTheOwnerBeingWaitedForWins(t *testing.T) {
	st := newStore(t)
	pr := trackPR(t, st, "owner/repo", 1)
	observeRequiredOwners(t, st, pr.ID, `["@org/api","@org/storage"]`)
	setChannel(t, st, "@org/api", "#api-reviews")
	setChannel(t, st, "@org/storage", "#storage-reviews")

	waitFor(t, st, pr.ID, "@org/storage")

	target, _ := channelFor(t, st, pr.ID)
	if target == nil {
		t.Fatal("ChannelFor found nothing")
	}
	if target.Channel != "#storage-reviews" || target.Why != ChannelFromWait {
		t.Errorf("target = %+v, want #storage-reviews from the wait", target)
	}
}

// TestTheRepositoryIsTheLastResort: the wrong grain, kept because plenty of
// repositories do have one right answer. Last, because it cannot tell a
// storage change from a docs one.
func TestTheRepositoryIsTheLastResort(t *testing.T) {
	st := newStore(t)
	pr := trackPR(t, st, "owner/repo", 1)
	observeRequiredOwners(t, st, pr.ID, `["@org/storage"]`)
	setAnnounceChannel(t, st, "owner/repo", "#repo-reviews")

	target, considered := channelFor(t, st, pr.ID)
	if target == nil {
		t.Fatal("ChannelFor found nothing")
	}
	if target.Channel != "#repo-reviews" || target.Owner != "" {
		t.Errorf("target = %+v, want the repository's own channel", target)
	}
	if len(considered) != 1 || considered[0] != "@org/storage" {
		t.Errorf("considered = %v, want the owner that had no channel", considered)
	}

	// And an owner channel takes it back, which is what makes the repository a
	// fallback rather than a default.
	setChannel(t, st, "@org/storage", "#storage-reviews")
	target, _ = channelFor(t, st, pr.ID)
	if target.Channel != "#storage-reviews" {
		t.Errorf("channel = %q, want the owner's once it exists", target.Channel)
	}
}

// TestNothingKnowsWhereItGoes reports the owners it looked at, because the fix
// is one `roz owner set` away and naming them is what saves reading CODEOWNERS
// to find out which.
func TestNothingKnowsWhereItGoes(t *testing.T) {
	st := newStore(t)
	pr := trackPR(t, st, "owner/repo", 1)
	observeRequiredOwners(t, st, pr.ID, `["@org/api","@org/storage"]`)

	target, considered := channelFor(t, st, pr.ID)
	if target != nil {
		t.Fatalf("ChannelFor invented %+v", target)
	}
	if len(considered) != 2 {
		t.Errorf("considered = %v, want both owners", considered)
	}
}

// setHints records a repository's preferred owners.
func setHints(t *testing.T, st *Store, repo string, hints []string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	r, err := tx.LoadGitHubRepo(ctx, repo)
	if err != nil {
		t.Fatalf("LoadGitHubRepo() returned error: %v", err)
	}
	if err := tx.SetOwnerHints(ctx, r, hints); err != nil {
		t.Fatalf("SetOwnerHints() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// setAnnounceChannel records the repository's own channel.
func setAnnounceChannel(t *testing.T, st *Store, repo, channel string) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	before, err := tx.LoadGitHubRepo(ctx, repo)
	if err != nil {
		t.Fatalf("LoadGitHubRepo() returned error: %v", err)
	}
	after := before.Clone()
	after.AnnounceChannel = sql.NullString{String: channel, Valid: true}
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// waitFor creates an open wait against a pull request, naming the owner it is
// for.
func waitFor(t *testing.T, st *Store, prID, owner string) {
	t.Helper()
	ctx := context.Background()

	a := NewAction("wait for "+owner, "wait_review")
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
}
