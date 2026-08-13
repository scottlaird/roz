package store

import (
	"context"
	"testing"
)

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"v*.*.0", "v1.5.0", true},
		{"v*.*.0", "v1.10.0", true},
		{"v*.*.0", "v10.0.0", true},
		// The case that keeps the pattern honest: a patch release is not a
		// minor one, and .10 is not .0 however similar the text looks.
		{"v*.*.0", "v1.5.10", false},
		{"v*.*.0", "v1.5.1", false},
		// A pre-release is not the release.
		{"v*.*.0", "v1.5.0-rc1", false},
		{"v*.*.0", "1.5.0", false},

		{"release-*", "release-1.5", true},
		{"release-*", "release-", true},
		{"release-*", "releas", false},

		{"v1.5.0", "v1.5.0", true},
		{"v1.5.0", "v1.5.1", false},

		{"*", "anything", true},
		{"*", "", true},
		{"v?.0", "v1.0", true},
		{"v?.0", "v10.0", false},

		// Directory-prefixed tags, as a monorepo uses. `*` spans `/`, which is
		// what makes api/v*.*.0 mean what somebody writing it expects; the
		// literal prefix is what keeps the two series apart.
		{"api/v*.*.0", "api/v3.6.0", true},
		{"api/v*.*.0", "v3.6.0", false},
		{"v*.*.0", "api/v3.6.0", false},
		{"service/s3/v*.*.0", "service/s3/v1.107.0", true},
		{"service/s3/v*.*.0", "service/sns/v1.107.0", false},

		// Several stars, which is where a naive matcher goes quadratic or
		// wrong. It should simply work.
		{"v*.*.*", "v1.2.3", true},
		{"*a*b*c*", "xxaxxbxxcxx", true},
		{"*a*b*c*", "xxaxxcxxbxx", false},
	}

	for _, tt := range tests {
		if got := globMatch(tt.pattern, tt.name); got != tt.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
		}
	}
}

// TestParseRefSpec covers the split: everything before the final slash is the
// path, everything after it is what to match.
func TestParseRefSpec(t *testing.T) {
	tests := []struct {
		spec            string
		prefix, matcher string
	}{
		{">=1.2", "", ">=1.2"},
		{"api/>=3.6", "api", ">=3.6"},
		{"service/s3/>=1.107", "service/s3", ">=1.107"},
		{"release-1.5", "", "release-1.5"},
		{"^1.2, <2.0", "", "^1.2, <2.0"},
		{"  >=1.2  ", "", ">=1.2"},
	}
	for _, tt := range tests {
		prefix, matcher, err := ParseRefSpec(tt.spec)
		if err != nil {
			t.Errorf("ParseRefSpec(%q) returned error: %v", tt.spec, err)
			continue
		}
		if prefix != tt.prefix || matcher != tt.matcher {
			t.Errorf("ParseRefSpec(%q) = %q, %q; want %q, %q",
				tt.spec, prefix, matcher, tt.prefix, tt.matcher)
		}
	}

	for _, spec := range []string{"", "   ", "api/"} {
		if _, _, err := ParseRefSpec(spec); err == nil {
			t.Errorf("ParseRefSpec(%q) was accepted, but there is nothing to wait for", spec)
		}
	}
}

// TestConstraintOrGlob: the two forms are told apart by whether the matcher
// parses, and they do not overlap.
func TestConstraintOrGlob(t *testing.T) {
	constraints := []string{">=1.2", "^1.2", "~1.2.3", "1.2.x", ">=1.2, <2.0", "*", "1.5", "v1.2.0"}
	globs := []string{"release-1.5", "release-*", "main", "hotfix-2024"}

	for _, m := range constraints {
		if _, ok := (RefWait{Matcher: m}).Constraint(); !ok {
			t.Errorf("%q should be read as a constraint", m)
		}
	}
	for _, m := range globs {
		if _, ok := (RefWait{Matcher: m}).Constraint(); ok {
			t.Errorf("%q should not be read as a constraint", m)
		}
	}
}

// TestRefWaitMatches covers the requirement the table exists for: "the next
// release", written before anyone knows its number.
func TestRefWaitMatches(t *testing.T) {
	wait := RefWait{RepoID: "acme/api", Kind: RefTag, Matcher: ">=1.5"}

	tests := []struct {
		name string
		ref  *GitRef
		want bool
	}{
		{"the next minor release", NewGitRef("acme/api", RefTag, "v1.5.0", "sha"), true},
		{"a later one", NewGitRef("acme/api", RefTag, "v2.0.0", "sha"), true},
		{"a patch of it", NewGitRef("acme/api", RefTag, "v1.5.3", "sha"), true},

		// The constraint is what stops a release that shipped long ago from
		// closing the action the moment the repository is first polled.
		{"a release that already shipped", NewGitRef("acme/api", RefTag, "v1.4.0", "sha"), false},
		{"a patch of the old line", NewGitRef("acme/api", RefTag, "v1.4.9", "sha"), false},

		{"without the v", NewGitRef("acme/api", RefTag, "1.5.0", "sha"), true},
		{"a branch of the same name", NewGitRef("acme/api", RefBranch, "v1.5.0", "sha"), false},
		{"another repository", NewGitRef("other/api", RefTag, "v1.5.0", "sha"), false},
		{"not a version at all", NewGitRef("acme/api", RefTag, "nightly", "sha"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wait.Matches(tt.ref); got != tt.want {
				t.Errorf("Matches(%s) = %v, want %v", tt.ref.Name, got, tt.want)
			}
		})
	}
}

// TestSeriesNeverCompareAcross is the monorepo requirement, and the reason the
// path is compared for equality rather than being part of a glob.
//
// A repository's v1.2.3 and its api/v3.4.5 are separate series that happen to
// share a repository. Their version numbers mean nothing to each other, so a
// wait on one must never be satisfied by the other however the numbers fall —
// and api/v3.4.5 is numerically far above any top-level 1.x.
func TestSeriesNeverCompareAcross(t *testing.T) {
	top := RefWait{RepoID: "acme/api", Kind: RefTag, Matcher: ">=1.2"}
	api := RefWait{RepoID: "acme/api", Kind: RefTag, PathPrefix: "api", Matcher: ">=3.6"}
	s3 := RefWait{RepoID: "acme/api", Kind: RefTag, PathPrefix: "service/s3", Matcher: ">=1.107"}

	topRef := NewGitRef("acme/api", RefTag, "v1.4.0", "sha")
	apiRef := NewGitRef("acme/api", RefTag, "api/v3.6.0", "sha")
	s3Ref := NewGitRef("acme/api", RefTag, "service/s3/v1.107.0", "sha")

	// Each matches its own.
	for _, c := range []struct {
		wait RefWait
		ref  *GitRef
	}{{top, topRef}, {api, apiRef}, {s3, s3Ref}} {
		if !c.wait.Matches(c.ref) {
			t.Errorf("%q did not match %s", c.wait.Spec(), c.ref.Name)
		}
	}

	// And nothing matches anybody else's, in either direction.
	for _, c := range []struct {
		wait RefWait
		ref  *GitRef
	}{
		{top, apiRef}, {top, s3Ref},
		{api, topRef}, {api, s3Ref},
		{s3, topRef}, {s3, apiRef},
	} {
		if c.wait.Matches(c.ref) {
			t.Errorf("%q was satisfied by %s, a different series", c.wait.Spec(), c.ref.Name)
		}
	}
}

// TestAnUnprefixedWaitIsTopLevelOnly states the rule on its own, because it is
// the one somebody would most easily get wrong: an absent prefix means the top
// level, not "any prefix".
func TestAnUnprefixedWaitIsTopLevelOnly(t *testing.T) {
	wait := RefWait{RepoID: "acme/api", Kind: RefTag, Matcher: ">=1.2"}

	// Numerically these all satisfy >=1.2. None of them is a top-level tag.
	for _, name := range []string{"api/v2.3.4", "api/v1.2.0", "service/s3/v9.9.9", "a/b/c/v5.0.0"} {
		if wait.Matches(NewGitRef("acme/api", RefTag, name, "sha")) {
			t.Errorf("an unprefixed wait was satisfied by %s", name)
		}
	}
}

// TestPrereleasesFollowTheConstraint: the rule is the constraint's own, which
// is most of the reason for expressing waits this way.
func TestPrereleasesFollowTheConstraint(t *testing.T) {
	release := NewGitRef("acme/api", RefTag, "v1.3.0", "sha")
	candidate := NewGitRef("acme/api", RefTag, "v1.3.0-rc1", "sha")

	plain := RefWait{RepoID: "acme/api", Kind: RefTag, Matcher: ">=1.2"}
	if plain.Matches(candidate) {
		t.Error(">=1.2 was satisfied by a release candidate")
	}
	if !plain.Matches(release) {
		t.Error(">=1.2 was not satisfied by the release")
	}

	// The published way to ask for them.
	including := RefWait{RepoID: "acme/api", Kind: RefTag, Matcher: ">=1.2.0-0"}
	if !including.Matches(candidate) {
		t.Error(">=1.2.0-0 did not match a release candidate")
	}

	// And within a series, a candidate is below its release.
	after := RefWait{RepoID: "acme/api", Kind: RefTag, Matcher: ">1.3.0-rc1, <=1.3.0"}
	if !after.Matches(release) {
		t.Error("v1.3.0 does not sort above v1.3.0-rc1")
	}
	if after.Matches(candidate) {
		t.Error("a candidate matched a constraint that excludes it")
	}
}

// TestAGlobWaitsForWhatHasNoVersion: the release-1.5 branch being cut, which
// no constraint can express.
func TestAGlobWaitsForWhatHasNoVersion(t *testing.T) {
	wait := RefWait{RepoID: "acme/api", Kind: RefBranch, Matcher: "release-1.5"}

	if !wait.Matches(NewGitRef("acme/api", RefBranch, "release-1.5", "sha")) {
		t.Error("a literal wait did not match the branch it names")
	}
	if wait.Matches(NewGitRef("acme/api", RefBranch, "release-1.6", "sha")) {
		t.Error("a literal wait matched a different branch")
	}

	globbed := RefWait{RepoID: "acme/api", Kind: RefBranch, Matcher: "release-*"}
	if !globbed.Matches(NewGitRef("acme/api", RefBranch, "release-1.6", "sha")) {
		t.Error("a glob did not match")
	}
	// Still confined to its own path, like every other wait.
	if globbed.Matches(NewGitRef("acme/api", RefBranch, "team/release-1.6", "sha")) {
		t.Error("a top-level glob matched a prefixed branch")
	}
}

// TestPollFilter: the series is asked for as a ref prefix, so this is only the
// leftover question of narrowing within one.
func TestPollFilter(t *testing.T) {
	tests := []struct{ prefix, matcher, want string }{
		// A constraint is not a substring of any ref name.
		{"", ">=1.0", ""},
		{"api", ">=1.0", ""},
		{"service/s3", ">=1.0", ""},
		// A name-shaped matcher has a literal head worth narrowing on.
		{"", "release-1.5", "release-1.5"},
		{"", "release-*", "release-"},
		{"team", "release-*", "release-"},
	}
	for _, tt := range tests {
		got := RefWait{PathPrefix: tt.prefix, Matcher: tt.matcher}.PollFilter()
		if got != tt.want {
			t.Errorf("PollFilter(%q, %q) = %q, want %q", tt.prefix, tt.matcher, got, tt.want)
		}
	}
}

func TestRefWaitSpec(t *testing.T) {
	tests := []struct {
		wait RefWait
		want string
	}{
		{RefWait{Matcher: ">=1.2"}, ">=1.2"},
		{RefWait{PathPrefix: "api", Matcher: ">=3.6"}, "api/>=3.6"},
		{RefWait{PathPrefix: "service/s3", Matcher: ">=1.107"}, "service/s3/>=1.107"},
	}
	for _, tt := range tests {
		if got := tt.wait.Spec(); got != tt.want {
			t.Errorf("Spec() = %q, want %q", got, tt.want)
		}
	}
}

// TestObserveRefRecordsTheFirstSighting: a ref appearing is news, so it is an
// event, and seeing it again is not.
func TestObserveRefRecordsTheFirstSighting(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	trackRepo(t, st, "acme/api")

	first := observeRef(t, st, NewGitRef("acme/api", RefTag, "v1.5.0", "abc123"))
	if !first {
		t.Error("the first sighting was not reported as new")
	}
	again := observeRef(t, st, NewGitRef("acme/api", RefTag, "v1.5.0", "abc123"))
	if again {
		t.Error("seeing the same ref again was reported as new")
	}

	created := 0
	for _, e := range events(t, st) {
		if e.SubjectType == "git_ref" && e.Kind == "created" {
			created++
		}
	}
	if created != 1 {
		t.Errorf("logged %d created events for one ref, want 1", created)
	}

	// A branch moves, and that is an observed column changing rather than a
	// new ref.
	observeRef(t, st, NewGitRef("acme/api", RefTag, "v1.5.0", "def456"))

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	ref, err := tx.LoadGitRef(ctx, RefID("acme/api", RefTag, "v1.5.0"))
	if err != nil {
		t.Fatalf("LoadGitRef() returned error: %v", err)
	}
	if got, want := ref.CommitSHA, "def456"; got != want {
		t.Errorf("commit_sha = %q, want %q", got, want)
	}
}

func observeRef(t *testing.T, st *Store, ref *GitRef) bool {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	isNew, err := tx.ObserveRef(ctx, ref)
	if err != nil {
		t.Fatalf("ObserveRef() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	return isNew
}

func setWait(t *testing.T, st *Store, w RefWait) {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.SetRefWait(ctx, w); err != nil {
		t.Fatalf("SetRefWait() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

// TestSettleClosesAWaitOnTheRefItNamed is the feature end to end: nothing
// says the release shipped, GitHub does, and the action goes away.
func TestSettleClosesAWaitOnTheRefItNamed(t *testing.T) {
	st := newStore(t)
	trackRepo(t, st, "acme/api")

	a := addAction(t, st, "wait for the next release", "wait_ref")
	setWait(t, st, RefWait{
		ActionID: a.ID, RepoID: "acme/api", Kind: RefTag, Matcher: ">=1.5",
	})

	// A repository nobody has polled closes nothing: no refs looks exactly
	// like a release that has not been cut, and only one of those means done.
	if settled := settle(t, st); len(settled) != 0 {
		t.Fatalf("Settle() closed %v before any ref was observed", settledIDs(settled))
	}

	// The release that already shipped is still in the repository, and is the
	// thing the constraint exists to exclude.
	observeRef(t, st, NewGitRef("acme/api", RefTag, "v1.4.0", "old"))
	if settled := settle(t, st); len(settled) != 0 {
		t.Fatalf("Settle() closed %v on a release the constraint excludes", settledIDs(settled))
	}

	// So is a release candidate for the one being waited for.
	observeRef(t, st, NewGitRef("acme/api", RefTag, "v1.5.0-rc1", "rc"))
	if settled := settle(t, st); len(settled) != 0 {
		t.Fatalf("Settle() closed %v on a release candidate", settledIDs(settled))
	}

	observeRef(t, st, NewGitRef("acme/api", RefTag, "v1.5.0", "new"))

	settled := settle(t, st)
	if len(settled) != 1 || settled[0].Action.ID != a.ID {
		t.Fatalf("Settle() closed %v, want [%s]", settledIDs(settled), a.ID)
	}
	if settled[0].Action.State != ActionDone {
		t.Errorf("state = %q, want %q", settled[0].Action.State, ActionDone)
	}
	// Nothing to name: this closed on a repository and an expression.
	if settled[0].PR != "" {
		t.Errorf("PR = %q, want empty", settled[0].PR)
	}
}

// TestAWaitWithNothingToWaitForIsNotACandidate: an action whose wait has not
// been written yet must not close and must not fail. It is simply not asked.
func TestAWaitWithNothingToWaitForIsNotACandidate(t *testing.T) {
	st := newStore(t)
	trackRepo(t, st, "acme/api")
	addAction(t, st, "wait for something unstated", "wait_ref")

	observeRef(t, st, NewGitRef("acme/api", RefTag, "v9.9.0", "sha"))

	if settled := settle(t, st); len(settled) != 0 {
		t.Errorf("Settle() closed %v for an action that never said what it waits for",
			settledIDs(settled))
	}
}

// TestOpenRefWaitsIgnoresClosedActions: the poll set is what sync asks GitHub
// about, so a finished wait must stop costing a query.
func TestOpenRefWaitsIgnoresClosedActions(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	trackRepo(t, st, "acme/api")

	a := addAction(t, st, "wait for the next api release", "wait_ref")
	setWait(t, st, RefWait{
		ActionID: a.ID, RepoID: "acme/api", Kind: RefTag,
		PathPrefix: "api", Matcher: ">=3.6",
	})

	waits, err := st.OpenRefWaits(ctx)
	if err != nil {
		t.Fatalf("OpenRefWaits() returned error: %v", err)
	}
	if len(waits) != 1 {
		t.Fatalf("OpenRefWaits() = %d waits, want 1", len(waits))
	}
	if got, want := waits[0].Describe(), "acme/api tag api/>=3.6"; got != want {
		t.Errorf("Describe() = %q, want %q", got, want)
	}

	closeIt(t, st, CloseRequest{ID: a.ID})

	waits, err = st.OpenRefWaits(ctx)
	if err != nil {
		t.Fatalf("OpenRefWaits() returned error: %v", err)
	}
	if len(waits) != 0 {
		t.Errorf("OpenRefWaits() still returns %d after the action closed", len(waits))
	}
}

// TestSetRefWaitReplaces: correcting a mistyped expression should not need the
// old row deleted first, and must not leave two.
func TestSetRefWaitReplaces(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	trackRepo(t, st, "acme/api")

	a := addAction(t, st, "wait for the next release", "wait_ref")
	setWait(t, st, RefWait{ActionID: a.ID, RepoID: "acme/api", Kind: RefTag, Matcher: ">=1.4"})
	setWait(t, st, RefWait{
		ActionID: a.ID, RepoID: "acme/api", Kind: RefTag,
		PathPrefix: "api", Matcher: ">=3.6",
	})

	waits, err := st.OpenRefWaits(ctx)
	if err != nil {
		t.Fatalf("OpenRefWaits() returned error: %v", err)
	}
	if len(waits) != 1 {
		t.Fatalf("OpenRefWaits() = %d waits, want 1", len(waits))
	}
	if got, want := waits[0].Spec(), "api/>=3.6"; got != want {
		t.Errorf("spec = %q, want %q", got, want)
	}
}

// TestABranchNamedLikeAConstraint is the trap the union reading exists for.
//
// facebook/react has a branch called `releases/19.2.x`. Written as a wait,
// that matcher parses as the constraint 19.2.* — and the branch's own name,
// "19.2.x", is not a version, so a constraint-only reading would never match
// the very ref it was written to name.
func TestABranchNamedLikeAConstraint(t *testing.T) {
	names := []string{"releases/19.2.x", "release/19.1.1", "release-1.5", "main"}

	for _, name := range names {
		prefix, matcher, err := ParseRefSpec(name)
		if err != nil {
			t.Fatalf("ParseRefSpec(%q) returned error: %v", name, err)
		}
		w := RefWait{
			RepoID: "acme/api", Kind: RefBranch,
			PathPrefix: prefix, Matcher: matcher,
		}
		if !w.Matches(NewGitRef("acme/api", RefBranch, name, "sha")) {
			t.Errorf("a wait written as %q does not match the branch it names", name)
		}
	}
}

// TestTheUnionAddsNoFalseMatches: a constraint-shaped matcher must not start
// matching refs literally, and the arms must stay disjoint where they should.
func TestTheUnionAddsNoFalseMatches(t *testing.T) {
	w := RefWait{RepoID: "acme/api", Kind: RefTag, Matcher: ">=1.2"}

	// The constraint still governs versions: nothing below it matches.
	for _, name := range []string{"1.1.0", "v1.1.9", "v1.1.9-rc1"} {
		if w.Matches(NewGitRef("acme/api", RefTag, name, "sha")) {
			t.Errorf(">=1.2 matched %q", name)
		}
	}
	// A ref literally named ">=1.2" would match, through the literal arm.
	// That is the union behaving as specified rather than a hole: no such ref
	// exists in practice, and "the ref you named" is the intended reading.
	if !w.Matches(NewGitRef("acme/api", RefTag, "v1.2.0", "sha")) {
		t.Error(">=1.2 stopped matching a version it covers")
	}

	// A name-shaped matcher stays literal: release-1.5 does not become a
	// constraint, so release-1.6 is not covered.
	lit := RefWait{RepoID: "acme/api", Kind: RefBranch, Matcher: "release-1.5"}
	if lit.Matches(NewGitRef("acme/api", RefBranch, "release-1.6", "sha")) {
		t.Error("a literal matcher matched a different branch")
	}
}

// TestABranchWaitNarrowsOnItsName: branches have no commit date to order by,
// so they are read alphabetically and a bounded read reaches only so far.
// facebook/react has 945 branches whose release ones sort past that, so
// without the literal head the ref is never fetched at all.
func TestABranchWaitNarrowsOnItsName(t *testing.T) {
	w := RefWait{RepoID: "facebook/react", Kind: RefBranch, Matcher: "releases/19.2.x"}
	// The series is a ref prefix, asked for exactly.
	if got := w.PathPrefix; got != "" {
		t.Errorf("PathPrefix = %q", got)
	}
	// And the name narrows within it.
	if got := w.PollFilter(); got != "releases/19.2.x" {
		t.Errorf("PollFilter() = %q, want the literal name", got)
	}
}
