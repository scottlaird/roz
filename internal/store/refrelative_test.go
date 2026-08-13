package store

import (
	"context"
	"testing"
)

func TestParseRelativeRef(t *testing.T) {
	ok := []struct {
		spec      string
		prefix    string
		component string
		offset    int
	}{
		{">=minor+2", "", ComponentMinor, 2},
		{">=major+1", "", ComponentMajor, 1},
		{">=patch+3", "", ComponentPatch, 3},
		{"api/>=minor+2", "api", ComponentMinor, 2},
		{"service/s3/>=minor+1", "service/s3", ComponentMinor, 1},
		{"  >=minor+2  ", "", ComponentMinor, 2},
	}
	for _, tt := range ok {
		got, err := ParseRelativeRef(tt.spec)
		if err != nil {
			t.Errorf("ParseRelativeRef(%q) returned error: %v", tt.spec, err)
			continue
		}
		if got.PathPrefix != tt.prefix || got.Component != tt.component || got.Offset != tt.offset {
			t.Errorf("ParseRelativeRef(%q) = %+v, want %s/%s+%d",
				tt.spec, got, tt.prefix, tt.component, tt.offset)
		}
	}

	bad := []string{
		"minor+2",     // no operator: the shape a wait takes is the shape a gate takes
		"<=minor+2",   // no reading: waiting for at most two minors on is not a thing
		"^minor+2",    //
		">=minor",     // no offset
		">=quarter+2", // not a version component
		">=minor+0",   // resolves to something that already exists
		">=minor+-1",  //
		">=minor+two", //
		"",
	}
	for _, spec := range bad {
		if _, err := ParseRelativeRef(spec); err == nil {
			t.Errorf("ParseRelativeRef(%q) was accepted", spec)
		}
	}
}

// TestARelativeGateIsNotAConstraint is what keeps the two forms apart: a step
// spec is read as a version constraint if it is one, and as a gate otherwise,
// so which it is never depends on context.
func TestARelativeGateIsNotAConstraint(t *testing.T) {
	for _, spec := range []string{">=minor+2", ">=major+1", "api/>=patch+3"} {
		_, matcher, err := ParseRefSpec(spec)
		if err != nil {
			t.Fatalf("ParseRefSpec(%q) returned error: %v", spec, err)
		}
		if _, isConstraint := (RefWait{Matcher: matcher}).Constraint(); isConstraint {
			t.Errorf("%q reads as an absolute constraint", spec)
		}
	}
	// And the absolute form still does.
	if _, isConstraint := (RefWait{Matcher: ">=3.6"}).Constraint(); !isConstraint {
		t.Error(">=3.6 stopped reading as a constraint")
	}
}

func TestRelativeRefResolve(t *testing.T) {
	refs := func(names ...string) []*GitRef {
		out := make([]*GitRef, len(names))
		for i, name := range names {
			out[i] = NewGitRef("acme/api", RefTag, name, "sha")
		}
		return out
	}

	tests := []struct {
		name string
		spec string
		refs []*GitRef
		want string
	}{
		{
			// The case from the issue: high-water v1.7.5, gate minor+2.
			name: "two minors on from the highest release",
			spec: ">=minor+2",
			refs: refs("v1.7.5", "v1.7.4", "v1.6.0"),
			want: ">=1.9.0",
		},
		{
			// Everything below the bumped component is zeroed: a release is
			// the whole of its line, and the patch somebody happened to be on
			// says nothing about where the next line starts.
			name: "the patch is not carried over",
			spec: ">=minor+1",
			refs: refs("v2.3.9"),
			want: ">=2.4.0",
		},
		{
			name: "a major bump zeroes the rest",
			spec: ">=major+1",
			refs: refs("v1.7.5"),
			want: ">=2.0.0",
		},
		{
			name: "a patch bump keeps its line",
			spec: ">=patch+1",
			refs: refs("v1.7.5"),
			want: ">=1.7.6",
		},
		{
			// Order is by version, not by the order rows come back in.
			name: "the highest wins, not the last",
			spec: ">=minor+1",
			refs: refs("v1.10.0", "v1.9.0", "v1.2.0"),
			want: ">=1.11.0",
		},
		{
			// A pre-release says a line is being prepared, not that it
			// happened, so counting from it would gate on the wrong release.
			name: "pre-releases are not counted from",
			spec: ">=minor+1",
			refs: refs("v1.8.0-rc1", "v1.7.0"),
			want: ">=1.8.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := ParseRelativeRef(tt.spec)
			if err != nil {
				t.Fatalf("ParseRelativeRef(%q) returned error: %v", tt.spec, err)
			}
			wait, ok := r.Resolve(tt.refs)
			if !ok {
				t.Fatalf("Resolve() found nothing to count from")
			}
			if wait.Matcher != tt.want {
				t.Errorf("Resolve() = %q, want %q", wait.Matcher, tt.want)
			}
		})
	}
}

// TestAGateCountsWithinItsSeries: a monorepo's api/ and its top level are
// unrelated, so a gate on one must not count from the other.
func TestAGateCountsWithinItsSeries(t *testing.T) {
	refs := []*GitRef{
		NewGitRef("acme/api", RefTag, "v1.7.5", "sha"),
		NewGitRef("acme/api", RefTag, "api/v3.4.0", "sha"),
		NewGitRef("acme/api", RefTag, "service/s3/v9.9.9", "sha"),
	}

	top, _ := ParseRelativeRef(">=minor+1")
	wait, ok := top.Resolve(refs)
	if !ok || wait.Matcher != ">=1.8.0" {
		t.Errorf("top-level gate = %q, want >=1.8.0", wait.Matcher)
	}

	api, _ := ParseRelativeRef("api/>=minor+1")
	wait, ok = api.Resolve(refs)
	if !ok || wait.Matcher != ">=3.5.0" {
		t.Errorf("api gate = %q, want >=3.5.0", wait.Matcher)
	}
	if wait.PathPrefix != "api" {
		t.Errorf("the resolved wait lost its series: %q", wait.PathPrefix)
	}
}

// TestAGateWithNothingToCountFrom waits rather than failing: a repository
// whose first release has not happened cannot answer yet, and will later.
func TestAGateWithNothingToCountFrom(t *testing.T) {
	r, _ := ParseRelativeRef(">=minor+2")

	if _, ok := r.Resolve(nil); ok {
		t.Error("Resolve() answered from no refs at all")
	}
	// Nor from refs no version scheme describes.
	if _, ok := r.Resolve([]*GitRef{NewGitRef("acme/api", RefTag, "nightly", "sha")}); ok {
		t.Error("Resolve() answered from a ref that is not a version")
	}
}

// TestAPendingGateResolvesOnceAndIsFrozen is the property the whole design
// rests on. A bound re-derived each poll would move its own goalposts: every
// release that shipped would push the target out and the gate would never
// open.
func TestAPendingGateResolvesOnceAndIsFrozen(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	trackRepo(t, st, "acme/api")

	a := addAction(t, st, "wait for a ref >=minor+2 in acme/api", "wait_ref")
	addPending(t, st, PendingRef{
		ActionID: a.ID, RepoID: "acme/api", Kind: RefTag, Spec: ">=minor+2",
	})
	observeRef(t, st, NewGitRef("acme/api", RefTag, "v1.7.5", "sha"))

	pending, err := st.PendingRefs(ctx)
	if err != nil {
		t.Fatalf("PendingRefs() returned error: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("PendingRefs() = %d, want 1", len(pending))
	}

	resolved, err := st.ResolvePendingRef(ctx, pending[0])
	if err != nil {
		t.Fatalf("ResolvePendingRef() returned error: %v", err)
	}
	if resolved == nil || resolved.Wait.Matcher != ">=1.9.0" {
		t.Fatalf("resolved to %+v, want >=1.9.0", resolved)
	}

	// The gate is gone, so nothing re-derives it.
	after, err := st.PendingRefs(ctx)
	if err != nil {
		t.Fatalf("PendingRefs() returned error: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("the gate is still pending after being resolved")
	}

	// Releases keep shipping, and the bound does not move.
	observeRef(t, st, NewGitRef("acme/api", RefTag, "v1.8.0", "sha"))
	observeRef(t, st, NewGitRef("acme/api", RefTag, "v1.9.0", "sha"))
	waits, err := st.OpenRefWaits(ctx)
	if err != nil {
		t.Fatalf("OpenRefWaits() returned error: %v", err)
	}
	if len(waits) != 1 || waits[0].Matcher != ">=1.9.0" {
		t.Fatalf("the bound moved to %+v", waits)
	}

	// And it is now satisfiable, which a moving one never would be.
	if settled := settle(t, st); len(settled) != 1 || settled[0].Action.ID != a.ID {
		t.Errorf("Settle() closed %v, want [%s]", settledIDs(settled), a.ID)
	}
}

// TestResolvingRetitlesTheQueueItem: the title carries the rule until the rule
// has an answer, and the queue should then say what is being waited for.
func TestResolvingRetitlesTheQueueItem(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	trackRepo(t, st, "acme/api")
	observeRef(t, st, NewGitRef("acme/api", RefTag, "v1.7.5", "sha"))

	a := addAction(t, st, "wait for a ref >=minor+2 in acme/api", "wait_ref")
	addPending(t, st, PendingRef{
		ActionID: a.ID, RepoID: "acme/api", Kind: RefTag, Spec: ">=minor+2",
	})

	pending, _ := st.PendingRefs(ctx)
	if _, err := st.ResolvePendingRef(ctx, pending[0]); err != nil {
		t.Fatalf("ResolvePendingRef() returned error: %v", err)
	}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()
	reloaded, err := tx.LoadAction(ctx, a.ID)
	if err != nil {
		t.Fatalf("LoadAction() returned error: %v", err)
	}
	if want := "wait for a ref >=1.9.0 in acme/api"; reloaded.Title != want {
		t.Errorf("title = %q, want %q", reloaded.Title, want)
	}
}

// TestAnEditedTitleIsLeftAlone: a title somebody has rewritten is theirs.
func TestAnEditedTitleIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	trackRepo(t, st, "acme/api")
	observeRef(t, st, NewGitRef("acme/api", RefTag, "v1.7.5", "sha"))

	a := addAction(t, st, "hold until the platform team is ready", "wait_ref")
	addPending(t, st, PendingRef{
		ActionID: a.ID, RepoID: "acme/api", Kind: RefTag, Spec: ">=minor+2",
	})

	pending, _ := st.PendingRefs(ctx)
	if _, err := st.ResolvePendingRef(ctx, pending[0]); err != nil {
		t.Fatalf("ResolvePendingRef() returned error: %v", err)
	}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()
	reloaded, _ := tx.LoadAction(ctx, a.ID)
	if reloaded.Title != "hold until the platform team is ready" {
		t.Errorf("title = %q, want it left alone", reloaded.Title)
	}
}

func addPending(t *testing.T, st *Store, p PendingRef) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.AddPendingRef(ctx, p); err != nil {
		t.Fatalf("AddPendingRef() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}
