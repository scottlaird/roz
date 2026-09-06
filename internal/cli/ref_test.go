package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/scottlaird/roz/internal/store"
)

// observeRefs records tags the way a sync would, so a test can ask what a
// release gate counts from without running one.
func observeRefs(t *testing.T, db, repo string, names ...string) {
	t.Helper()

	st, err := store.OpenStore(context.Background(), db)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	tx, err := st.Begin(ctx, store.ActorSyncGitHub)
	if err != nil {
		t.Fatalf("beginning a transaction: %v", err)
	}
	defer tx.Rollback()

	for _, name := range names {
		// A full-length commit, so anything that abbreviates one is actually
		// abbreviating something.
		sha := "abc1234def5678901234567890abcdef12345678"
		if _, err := tx.ObserveRef(ctx, store.NewGitRef(repo, store.RefTag, name, sha)); err != nil {
			t.Fatalf("observing %s: %v", name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("committing the observed refs: %v", err)
	}
}

// pendingSpec returns the unresolved gate on an action, or "" when it has
// none.
func pendingSpec(t *testing.T, db, actionID string) string {
	t.Helper()

	st, err := store.OpenStore(context.Background(), db)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer st.Close()

	pending, err := st.PendingRefs(context.Background())
	if err != nil {
		t.Fatalf("reading the pending gates: %v", err)
	}
	for _, p := range pending {
		if p.ActionID == actionID {
			return p.Spec
		}
	}
	return ""
}

// waitMatcher returns what an action is waiting for, or "" when it waits for
// no ref.
func waitMatcher(t *testing.T, db, actionID string) string {
	t.Helper()

	st, err := store.OpenStore(context.Background(), db)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		t.Fatalf("beginning a transaction: %v", err)
	}
	defer tx.Rollback()

	wait, err := tx.RefWaitFor(ctx, actionID)
	if err != nil {
		t.Fatalf("reading what %s waits for: %v", actionID, err)
	}
	if wait == nil {
		return ""
	}
	return wait.Spec()
}

// TestWaitRefRefusesAVerbWithNothingToWaitFor: a wait_ref action that never
// says which ref would sit in the queue forever, so the add is refused with
// the flags that fix it.
func TestWaitRefRefusesAVerbWithNothingToWaitFor(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "action", "add", "--db", db,
		"--title", "Wait for the next release", "--verb", "wait_ref")
	if err == nil {
		t.Fatal("action add accepted a wait_ref with nothing to wait for")
	}
	for _, want := range []string{"ref-repo", "ref"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name --%s: %v", want, err)
		}
	}
}

// TestWaitRefNeedsBothHalves: a repository with nothing to wait for would
// match any ref at all, which is never what anybody meant.
func TestWaitRefNeedsBothHalves(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	_, err := runCLI(t, "action", "add", "--db", db,
		"--title", "Wait", "--verb", "wait_ref", "--ref-repo", "acme/api")
	if err == nil {
		t.Fatal("action add accepted a repository with nothing to wait for")
	}
	if !strings.Contains(err.Error(), "go together") {
		t.Errorf("error does not explain the pairing: %v", err)
	}
}

func TestAddAWaitAndSeeIt(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	id := addAction(t, db, "--title", "Wait for the next release", "--verb", "wait_ref",
		"--ref-repo", "acme/api", "--ref", ">=1.5")

	out, err := runCLI(t, "action", "list", "--db", db)
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if !strings.Contains(out, id) {
		t.Errorf("the action is not in the queue:\n%s", out)
	}

	// Nothing has been polled, so there is nothing to list yet — and the
	// listing has to say so rather than looking like an error.
	if _, err := runCLI(t, "ref", "list", "--db", db); err != nil {
		t.Fatalf("ref list returned error: %v", err)
	}
}

// TestWaitRefCorrectsAMistypedExpression: the wait is set on an existing
// action, which is the path `action wait-ref` exists for.
func TestWaitRefCorrectsAMistypedExpression(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	id := addAction(t, db, "--title", "Wait for the next release", "--verb", "wait_ref",
		"--ref-repo", "acme/api", "--ref", ">=1.4")

	out, err := runCLI(t, "action", "wait-ref", "--db", db, "--action", id,
		"--ref-repo", "acme/api", "--ref", "api/>=3.6")
	if err != nil {
		t.Fatalf("action wait-ref returned error: %v", err)
	}
	if want := "acme/api tag api/>=3.6"; !strings.Contains(out, want) {
		t.Errorf("wait-ref printed %q, want it to name %q", out, want)
	}
}

// TestWaitRefRefusesAnUntrackedRepository: a wait against a repository roz
// does not track would never be polled, so it is refused where it is written
// rather than being silently inert.
func TestWaitRefRefusesAnUntrackedRepository(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "action", "add", "--db", db,
		"--title", "Wait", "--verb", "wait_ref",
		"--ref-repo", "acme/api", "--ref", ">=1.5")
	if err == nil {
		t.Fatal("action add accepted a wait on an untracked repository")
	}
	if !strings.Contains(err.Error(), "acme/api") {
		t.Errorf("error does not name the repository: %v", err)
	}
}

// TestWaitRefRefusesAnUnknownKind: branch or tag, named in the error rather
// than surfaced as a CHECK constraint.
func TestWaitRefRefusesAnUnknownKind(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	_, err := runCLI(t, "action", "add", "--db", db,
		"--title", "Wait", "--verb", "wait_ref",
		"--ref-repo", "acme/api", "--ref", ">=1.5", "--ref-kind", "note")
	if err == nil {
		t.Fatal("action add accepted a ref kind that is not a branch or a tag")
	}
	if !strings.Contains(err.Error(), "branch") {
		t.Errorf("error does not name the alternatives: %v", err)
	}
}

// TestWaitRefRefusesAPathWithNothingToMatch: `api/` names a series and says
// nothing about which release, so it can never be satisfied.
func TestWaitRefRefusesAPathWithNothingToMatch(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	_, err := runCLI(t, "action", "add", "--db", db,
		"--title", "Wait", "--verb", "wait_ref",
		"--ref-repo", "acme/api", "--ref", "api/")
	if err == nil {
		t.Fatal("action add accepted a path with nothing to match")
	}
	if !strings.Contains(err.Error(), "nothing to match") {
		t.Errorf("error does not say what is missing: %v", err)
	}
}

// TestAGateResolvesAgainstTheReleasesAlreadyRead is the point of writing one
// by hand: "wait for the next major" is what somebody means, and the version
// it comes out as is roz's to work out rather than theirs to look up.
func TestAGateResolvesAgainstTheReleasesAlreadyRead(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")
	observeRefs(t, db, "acme/api", "v2.96.0", "v2.97.0", "v2.97.1")

	for _, tc := range []struct {
		spec string
		want string
	}{
		{">=major+1", ">=3.0.0"},
		{">=minor+2", ">=2.99.0"},
		{">=patch+1", ">=2.97.2"},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			id := addAction(t, db, "--title", "Ship it once the release is out",
				"--verb", "wait_ref", "--ref-repo", "acme/api", "--ref", tc.spec)

			if got := waitMatcher(t, db, id); got != tc.want {
				t.Errorf("%s resolved to %q, want %q", tc.spec, got, tc.want)
			}
			// Resolved, so there is nothing left for sync to work out.
			if got := pendingSpec(t, db, id); got != "" {
				t.Errorf("%s is still pending as %q after resolving", tc.spec, got)
			}
		})
	}
}

// TestAGateWaitsForTheReleasesToBeRead: a repository nothing has polled cannot
// say what the next release is, so the gate stands until sync reads its tags —
// which asking for it is also what makes happen.
func TestAGateWaitsForTheReleasesToBeRead(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	out, err := runCLI(t, "action", "add", "--db", db,
		"--title", "Wait for the next minor", "--verb", "wait_ref",
		"--ref-repo", "acme/api", "--ref", ">=minor+1")
	if err != nil {
		t.Fatalf("action add returned error: %v", err)
	}
	// The identifier still comes first, whatever is said after it.
	id, rest, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if id != "NA1" {
		t.Fatalf("action add printed %q first, want the identifier", id)
	}
	if !strings.Contains(rest, "once its releases have been read") {
		t.Errorf("add did not say the gate is unresolved:\n%s", out)
	}

	if got := pendingSpec(t, db, id); got != ">=minor+1" {
		t.Errorf("the gate is recorded as %q, want %q", got, ">=minor+1")
	}
	// The failure this replaces: stored as a wait, it is a matcher no ref can
	// equal, and the action waits for ever without saying so.
	if got := waitMatcher(t, db, id); got != "" {
		t.Errorf("the gate was stored as a wait for %q", got)
	}

	// `action wait-ref` says the same, and names the repository the tags will
	// be read from.
	out, err = runCLI(t, "action", "wait-ref", "--db", db, "--action", id,
		"--ref-repo", "acme/api", "--ref", ">=minor+2")
	if err != nil {
		t.Fatalf("action wait-ref returned error: %v", err)
	}
	for _, want := range []string{"acme/api", ">=minor+2", "once its releases have been read"} {
		if !strings.Contains(out, want) {
			t.Errorf("wait-ref did not mention %q:\n%s", want, out)
		}
	}
}

// TestAGateAndAWaitReplaceEachOther: an action waits for one thing. Correcting
// a gate to a version, or a version to a gate, has to leave exactly the one
// just written — the other would fire on its own and close the action on a
// release nobody is waiting for.
func TestAGateAndAWaitReplaceEachOther(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	id := addAction(t, db, "--title", "Wait", "--verb", "wait_ref",
		"--ref-repo", "acme/api", "--ref", ">=minor+1")

	if _, err := runCLI(t, "action", "wait-ref", "--db", db, "--action", id,
		"--ref-repo", "acme/api", "--ref", ">=4.0"); err != nil {
		t.Fatalf("action wait-ref returned error: %v", err)
	}
	if got := waitMatcher(t, db, id); got != ">=4.0" {
		t.Errorf("waits for %q, want %q", got, ">=4.0")
	}
	if got := pendingSpec(t, db, id); got != "" {
		t.Errorf("the replaced gate is still pending as %q", got)
	}

	if _, err := runCLI(t, "action", "wait-ref", "--db", db, "--action", id,
		"--ref-repo", "acme/api", "--ref", ">=major+1"); err != nil {
		t.Fatalf("action wait-ref returned error: %v", err)
	}
	if got := pendingSpec(t, db, id); got != ">=major+1" {
		t.Errorf("the gate is recorded as %q, want %q", got, ">=major+1")
	}
	if got := waitMatcher(t, db, id); got != "" {
		t.Errorf("the replaced wait for %q is still there", got)
	}
}

// TestARuleThatParsesAsNeitherIsRefused: a mistyped rule used to become a
// literal name, and an action waiting for a tag called ">=minr+1" waits for
// ever. Both spellings of the mistake are refused where they are written.
func TestARuleThatParsesAsNeitherIsRefused(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	for _, tc := range []struct {
		spec string
		want string
	}{
		// The component misspelt, which the gate parser can name.
		{">=minr+1", "major, minor, patch"},
		// The operator left off, which is how the rule reads aloud.
		{"minor+1", ">=component+n"},
		// Neither form, so the error has to offer both.
		{">=1.2.", "release gate"},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			_, err := runCLI(t, "action", "add", "--db", db,
				"--title", "Wait", "--verb", "wait_ref",
				"--ref-repo", "acme/api", "--ref", tc.spec)
			if err == nil {
				t.Fatalf("action add accepted %q", tc.spec)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error for %q does not mention %q: %v", tc.spec, tc.want, err)
			}
		})
	}
}

// TestANameIsStillANameNextToTheRules: the shapes are told apart by how they
// are written, so refusing a mistyped rule must not refuse a branch whose name
// is not a version at all.
func TestANameIsStillANameNextToTheRules(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	id := addAction(t, db, "--title", "Port the fix once 1.5 is branched",
		"--verb", "wait_ref", "--ref-repo", "acme/api",
		"--ref-kind", "branch", "--ref", "release-1.5")

	if got := waitMatcher(t, db, id); got != "release-1.5" {
		t.Errorf("waits for %q, want %q", got, "release-1.5")
	}
}

// TestAWaitOnAMonorepoSeries: the shape the path prefix exists for, end to end
// through the command.
func TestAWaitOnAMonorepoSeries(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	id := addAction(t, db, "--title", "Wait for the next s3 release", "--verb", "wait_ref",
		"--ref-repo", "acme/api", "--ref", "service/s3/>=1.107")

	out, err := runCLI(t, "action", "wait-ref", "--db", db, "--action", id,
		"--ref-repo", "acme/api", "--ref", "service/s3/>=1.108")
	if err != nil {
		t.Fatalf("action wait-ref returned error: %v", err)
	}
	if want := "service/s3/>=1.108"; !strings.Contains(out, want) {
		t.Errorf("wait-ref printed %q, want it to name %q", out, want)
	}
}
