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

// TestVersionOrdering is why names are not compared as text: v1.10.0 is above
// v1.5.0 as a version and below it as a string.
func TestVersionOrdering(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v1.10.0", "v1.5.0", 1},
		{"v1.5.0", "v1.10.0", -1},
		{"v1.5.0", "v1.5.0", 0},
		{"v2.0.0", "v1.99.99", 1},

		// The naming scheme does not have to be the same, which is the point
		// of extracting numbers rather than configuring a rule per repository.
		{"release-1.5.0", "v1.4.0", 1},
		{"1.5.0", "v1.5.0", 0},

		// A missing component is a zero, so v1.5 and v1.5.0 are one version.
		{"v1.5", "v1.5.0", 0},
		{"v1.5", "v1.5.1", -1},

		// No numbers at all sorts below anything with one.
		{"main", "v0.0.1", -1},
		{"main", "trunk", 0},
	}

	for _, tt := range tests {
		got := compareVersions(versionOf(tt.a), versionOf(tt.b))
		if got != tt.want {
			t.Errorf("compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

// TestRefWaitMatches covers the requirement the table exists for: "the next
// v1.N.0", written before anyone knows its number.
func TestRefWaitMatches(t *testing.T) {
	wait := RefWait{
		RepoID: "acme/api", Kind: RefTag, Pattern: "v*.*.0", After: "v1.4.7",
	}

	tests := []struct {
		name string
		ref  *GitRef
		want bool
	}{
		{"the next minor release", NewGitRef("acme/api", RefTag, "v1.5.0", "sha"), true},
		{"a later one", NewGitRef("acme/api", RefTag, "v2.0.0", "sha"), true},

		// The bound is what stops a release that shipped long ago from
		// closing the action the moment the repository is first polled.
		{"a release that already shipped", NewGitRef("acme/api", RefTag, "v1.4.0", "sha"), false},
		{"the bound itself", NewGitRef("acme/api", RefTag, "v1.4.7", "sha"), false},

		{"a patch release", NewGitRef("acme/api", RefTag, "v1.5.1", "sha"), false},
		{"a branch of the same name", NewGitRef("acme/api", RefBranch, "v1.5.0", "sha"), false},
		{"another repository", NewGitRef("other/api", RefTag, "v1.5.0", "sha"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wait.Matches(tt.ref); got != tt.want {
				t.Errorf("Matches(%s) = %v, want %v", tt.ref.Name, got, tt.want)
			}
		})
	}
}

// TestRefWaitWithoutBound: no bound means any matching name, which is right
// for "the v1.5 branch has been cut" and wrong for "the next release".
func TestRefWaitWithoutBound(t *testing.T) {
	wait := RefWait{RepoID: "acme/api", Kind: RefBranch, Pattern: "release-1.5"}

	if !wait.Matches(NewGitRef("acme/api", RefBranch, "release-1.5", "sha")) {
		t.Error("an unbounded wait did not match the ref it names")
	}
	if wait.Matches(NewGitRef("acme/api", RefBranch, "release-1.6", "sha")) {
		t.Error("an unbounded wait matched a name its pattern does not")
	}
}

func TestLiteralPrefix(t *testing.T) {
	tests := []struct{ pattern, want string }{
		{"v*.*.0", "v"},
		{"release-*", "release-"},
		{"v1.5.0", "v1.5.0"},
		{"*", ""},
		{"v?.0", "v"},
	}
	for _, tt := range tests {
		got := RefWait{Pattern: tt.pattern}.LiteralPrefix()
		if got != tt.want {
			t.Errorf("LiteralPrefix(%q) = %q, want %q", tt.pattern, got, tt.want)
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

// TestOpenRefWaitsIgnoresClosedActions: the poll set is what sync asks GitHub
// about, so a finished wait must stop costing a query.
func TestOpenRefWaitsIgnoresClosedActions(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	trackRepo(t, st, "acme/api")

	a := addAction(t, st, "wait for the next release", "wait_ref")
	setWait(t, st, RefWait{
		ActionID: a.ID, RepoID: "acme/api", Kind: RefTag, Pattern: "v*.*.0", After: "v1.4.7",
	})

	waits, err := st.OpenRefWaits(ctx)
	if err != nil {
		t.Fatalf("OpenRefWaits() returned error: %v", err)
	}
	if len(waits) != 1 {
		t.Fatalf("OpenRefWaits() = %d waits, want 1", len(waits))
	}
	if got, want := waits[0].Describe(), "acme/api tag v*.*.0 after v1.4.7"; got != want {
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

// TestSetRefWaitReplaces: correcting a mistyped pattern should not need the
// old row deleted first, and must not leave two.
func TestSetRefWaitReplaces(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	trackRepo(t, st, "acme/api")

	a := addAction(t, st, "wait for the next release", "wait_ref")
	setWait(t, st, RefWait{ActionID: a.ID, RepoID: "acme/api", Kind: RefTag, Pattern: "v*.*.1"})
	setWait(t, st, RefWait{ActionID: a.ID, RepoID: "acme/api", Kind: RefTag, Pattern: "v*.*.0"})

	waits, err := st.OpenRefWaits(ctx)
	if err != nil {
		t.Fatalf("OpenRefWaits() returned error: %v", err)
	}
	if len(waits) != 1 {
		t.Fatalf("OpenRefWaits() = %d waits, want 1", len(waits))
	}
	if got, want := waits[0].Pattern, "v*.*.0"; got != want {
		t.Errorf("pattern = %q, want %q", got, want)
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
		ActionID: a.ID, RepoID: "acme/api", Kind: RefTag, Pattern: "v*.*.0", After: "v1.4.7",
	})

	// A repository nobody has polled closes nothing: no refs looks exactly
	// like a release that has not been cut, and only one of those means done.
	if settled := settle(t, st); len(settled) != 0 {
		t.Fatalf("Settle() closed %v before any ref was observed", settledIDs(settled))
	}

	// The release that already shipped is still in the repository, and is the
	// thing the bound exists to exclude.
	observeRef(t, st, NewGitRef("acme/api", RefTag, "v1.4.0", "old"))
	if settled := settle(t, st); len(settled) != 0 {
		t.Fatalf("Settle() closed %v on a release that predates the bound", settledIDs(settled))
	}

	observeRef(t, st, NewGitRef("acme/api", RefTag, "v1.5.0", "new"))

	settled := settle(t, st)
	if len(settled) != 1 || settled[0].Action.ID != a.ID {
		t.Fatalf("Settle() closed %v, want [%s]", settledIDs(settled), a.ID)
	}
	if settled[0].Action.State != ActionDone {
		t.Errorf("state = %q, want %q", settled[0].Action.State, ActionDone)
	}
	// Nothing to name: this closed on a repository and a pattern.
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

// TestDirectoryPrefixedRefs covers the monorepo shape: a repository carrying
// several disjoint tag series, v1.2.3 alongside api/v3.4.5, where "the next
// api release" must not be confused with "the next release".
//
// Written because the first implementation passed this by coincidence. `*`
// spans `/`, so the globs happened to work; versionOf read the whole string,
// so `service/s3/v1.107.0` gave [3 1 107 0] — the 3 belonging to the
// component's name rather than to any version.
func TestDirectoryPrefixedRefs(t *testing.T) {
	t.Run("the version is the last segment", func(t *testing.T) {
		tests := []struct {
			name string
			want []int
		}{
			{"v1.2.3", []int{1, 2, 3}},
			{"api/v3.4.5", []int{3, 4, 5}},
			{"bigquery/v1.79.1", []int{1, 79, 1}},
			// The case that was wrong: a digit in the component name.
			{"service/s3/v1.107.0", []int{1, 107, 0}},
			{"v2/api/v1.0.0", []int{1, 0, 0}},
		}
		for _, tt := range tests {
			got := versionOf(tt.name)
			if len(got) != len(tt.want) {
				t.Errorf("versionOf(%q) = %v, want %v", tt.name, got, tt.want)
				continue
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("versionOf(%q) = %v, want %v", tt.name, got, tt.want)
					break
				}
			}
		}
	})

	t.Run("a bound may be written with or without the prefix", func(t *testing.T) {
		ref := NewGitRef("acme/api", RefTag, "service/s3/v1.107.0", "sha")
		for _, after := range []string{"service/s3/v1.106.0", "v1.106.0"} {
			w := RefWait{
				RepoID: "acme/api", Kind: RefTag,
				Pattern: "service/s3/v*.*.0", After: after,
			}
			if !w.Matches(ref) {
				t.Errorf("--ref-after %q did not match %s", after, ref.Name)
			}
		}
	})

	t.Run("the series stay disjoint", func(t *testing.T) {
		// The whole requirement: waiting for the next top-level release must
		// not be satisfied by an api release, and the reverse.
		top := RefWait{RepoID: "acme/api", Kind: RefTag, Pattern: "v*.*.0", After: "v1.2.3"}
		api := RefWait{RepoID: "acme/api", Kind: RefTag, Pattern: "api/v*.*.0", After: "api/v3.4.5"}

		apiRef := NewGitRef("acme/api", RefTag, "api/v3.6.0", "sha")
		topRef := NewGitRef("acme/api", RefTag, "v1.4.0", "sha")

		if top.Matches(apiRef) {
			t.Error("a wait for v*.*.0 was satisfied by api/v3.6.0")
		}
		if api.Matches(topRef) {
			t.Error("a wait for api/v*.*.0 was satisfied by v1.4.0")
		}
		if !top.Matches(topRef) {
			t.Error("a wait for v*.*.0 did not match v1.4.0")
		}
		if !api.Matches(apiRef) {
			t.Error("a wait for api/v*.*.0 did not match api/v3.6.0")
		}
	})

	t.Run("an api release still has to beat its own bound", func(t *testing.T) {
		w := RefWait{RepoID: "acme/api", Kind: RefTag, Pattern: "api/v*.*.0", After: "api/v3.4.5"}
		if w.Matches(NewGitRef("acme/api", RefTag, "api/v3.2.0", "sha")) {
			t.Error("an api release older than the bound matched")
		}
	})

	t.Run("the prefix narrows what GitHub is asked for", func(t *testing.T) {
		// LiteralPrefix becomes the GraphQL `query`, so a prefixed pattern has
		// to yield the directory rather than the empty string — otherwise a
		// monorepo's every tag comes back to be filtered here.
		tests := []struct{ pattern, want string }{
			{"api/v*.*.0", "api/v"},
			{"service/s3/v*.*.0", "service/s3/v"},
			{"v*.*.0", "v"},
		}
		for _, tt := range tests {
			if got := (RefWait{Pattern: tt.pattern}).LiteralPrefix(); got != tt.want {
				t.Errorf("LiteralPrefix(%q) = %q, want %q", tt.pattern, got, tt.want)
			}
		}
	})

	t.Run("an identifier keeps the two apart", func(t *testing.T) {
		// A tag and a branch may share a name, and so may two series.
		ids := map[string]bool{}
		for _, name := range []string{"v1.2.3", "api/v1.2.3"} {
			for _, kind := range []string{RefTag, RefBranch} {
				ids[RefID("acme/api", kind, name)] = true
			}
		}
		if len(ids) != 4 {
			t.Errorf("four refs produced %d identifiers", len(ids))
		}
	})
}

// TestPrereleasesDoNotSatisfyAWaitForTheRelease is the case v*.*.* used to get
// wrong twice over: the glob matched v1.2.0-rc1, and the digit comparison put
// it *above* v1.2.0 because [1 2 0 1] is greater than [1 2 0].
func TestPrereleasesDoNotSatisfyAWaitForTheRelease(t *testing.T) {
	pre := NewGitRef("acme/api", RefTag, "v1.2.0-pre1", "sha")
	release := NewGitRef("acme/api", RefTag, "v1.2.0", "sha")

	// Every way of writing "the next release", including the two whose globs
	// do match a pre-release.
	for _, pattern := range []string{"v1.2.0", "v*.*.0", "v1.2.*", "v*.*.*"} {
		w := RefWait{RepoID: "acme/api", Kind: RefTag, Pattern: pattern, After: "v1.1.0"}
		if w.Matches(pre) {
			t.Errorf("pattern %q was satisfied by a pre-release", pattern)
		}
		if !w.Matches(release) {
			t.Errorf("pattern %q was not satisfied by the release itself", pattern)
		}
	}
}

// TestAWaitMayAskForAPrerelease: the exclusion is a default, not a rule. A
// wait naming one is an unambiguous statement that they are the point.
func TestAWaitMayAskForAPrerelease(t *testing.T) {
	pre := NewGitRef("acme/api", RefTag, "v1.2.0-rc2", "sha")

	byPattern := RefWait{RepoID: "acme/api", Kind: RefTag, Pattern: "v1.2.0-rc*"}
	if !byPattern.Matches(pre) {
		t.Error("a wait whose pattern names a release candidate did not match one")
	}

	byBound := RefWait{
		RepoID: "acme/api", Kind: RefTag, Pattern: "v*.*.*", After: "v1.2.0-rc1",
	}
	if !byBound.Matches(pre) {
		t.Error("a wait bounded at a release candidate did not match a later one")
	}
	// And the ordering within the series is semver's, not the digits'.
	if byBound.Matches(NewGitRef("acme/api", RefTag, "v1.2.0-rc1", "sha")) {
		t.Error("the bound matched itself")
	}
}

// TestSemverOrdering covers what x/mod/semver buys over comparing digits.
func TestSemverOrdering(t *testing.T) {
	tests := []struct {
		a, b string
		want int
		why  string
	}{
		// The whole reason for the dependency: no ordering of extracted
		// digits puts a pre-release below its release.
		{"v1.2.0-rc1", "v1.2.0", -1, "a pre-release is below its release"},
		{"v1.2.0-rc1", "v1.2.0-rc2", -1, "release candidates order among themselves"},
		{"v1.2.0-rc.2", "v1.2.0-rc.10", -1, "dot-separated numbers order numerically"},
		{"v1.2.0-alpha", "v1.2.0-beta", -1, "alphanumeric identifiers order as text"},
		// Worth knowing rather than worth fixing: -rc2 and -rc10 are single
		// alphanumeric identifiers, so semver orders them as text and rc10
		// comes *below* rc2. That is the specification, not a defect here,
		// and -rc.10 is how to mean the tenth.
		{"v1.2.0-rc10", "v1.2.0-rc2", -1, "an undotted number is text, per the spec"},

		// Still true, now by a different route.
		{"v1.10.0", "v1.5.0", 1, "minor versions are numbers"},
		{"v1.2", "v1.2.0", 0, "a missing patch is zero"},
		{"v1.2.0+build9", "v1.2.0", 0, "build metadata is not a version"},

		// A bare X.Y.Z is normalised rather than dropped to the fallback.
		{"1.2.0", "v1.1.0", 1, "a missing v is supplied"},
		{"1.2.0-rc1", "1.2.0", -1, "and pre-releases still work without it"},

		// Not semver at all: the digit fallback, unchanged.
		{"release-1.5.0", "release-1.4.0", 1, "a scheme semver cannot read"},
		{"release-1.10.0", "release-1.5.0", 1, "and it still orders numerically"},

		// One side semver and the other not: both go through the fallback, so
		// the answer does not depend on which happened to parse.
		{"v1.5.0", "release-1.4.0", 1, "mixed schemes use one ordering"},
	}

	for _, tt := range tests {
		if got := compareRefVersions(tt.a, tt.b); got != tt.want {
			t.Errorf("compare(%q, %q) = %d, want %d — %s", tt.a, tt.b, got, tt.want, tt.why)
		}
	}
}

// TestPrereleaseDetection: only a semantic version has the notion, and a
// scheme that cannot be read excludes nothing.
func TestPrereleaseDetection(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"v1.2.0-rc1", true},
		{"1.2.0-rc1", true},
		{"api/v3.4.0-beta", true},
		{"v1.2.0", false},
		{"v1.2.0+build", false},
		// Not semver, so no pre-release either — the safe direction, since
		// the alternative is quietly ignoring what somebody waits for.
		{"release-1.5.0-rc1", false},
		{"main", false},
	}
	for _, tt := range tests {
		if got := isPrerelease(tt.name); got != tt.want {
			t.Errorf("isPrerelease(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
