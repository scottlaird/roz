package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// Kinds of ref roz observes.
//
// Not every kind git has: these are the two a person waits for. A note or a
// stash appearing is not a condition anybody gates work on.
const (
	RefBranch = "branch"
	RefTag    = "tag"
)

// RefKinds is the vocabulary, in the order a listing should show it.
var RefKinds = []string{RefBranch, RefTag}

// refPrefixes maps a kind to the ref namespace it lives in, which is what
// GitHub is asked for and what makes an identifier unambiguous.
var refPrefixes = map[string]string{
	RefBranch: "refs/heads/",
	RefTag:    "refs/tags/",
}

// refSeparator divides a repository from the ref path in an identifier.
const refSeparator = "@"

// ValidateRefKind refuses a kind the schema would refuse, naming the
// alternatives rather than quoting a CHECK constraint.
func ValidateRefKind(kind string) error {
	if _, ok := refPrefixes[kind]; ok {
		return nil
	}
	return fmt.Errorf("ref kind %q is not recognised: use %s",
		kind, strings.Join(RefKinds, " or "))
}

// RefPath renders the full ref, e.g. refs/tags/v1.5.0.
func RefPath(kind, name string) string { return refPrefixes[kind] + name }

// RefID composes the identifier: acme/api@refs/tags/v1.5.0.
//
// The full path rather than the short name, because a branch and a tag may
// share one and the identifier has to tell them apart on its own.
func RefID(repo, kind, name string) string {
	return repo + refSeparator + RefPath(kind, name)
}

// GitRef is a branch or tag, as GitHub last reported it.
//
// Observed in its entirety apart from the identity: which refs exist is not
// anybody's decision. Sync is the only writer, and the actor rule enforces it.
type GitRef struct {
	ID     string `db:"id" kind:"identity"`
	RepoID string `db:"repo_id" kind:"identity"`
	// Name is the short form a person writes: v1.5.0, not refs/tags/v1.5.0.
	Name string `db:"name" kind:"identity"`
	Kind string `db:"kind" kind:"identity"`

	CommitSHA string `db:"commit_sha" kind:"observed"`

	// FirstSeen is when roz first saw it, which is not when it was created.
	// A ref that existed before anything waited on it is first seen on the
	// day something did — so this dates the observation, not the release.
	FirstSeen  string `db:"first_seen" kind:"created"`
	ObservedAt string `db:"observed_at" kind:"auto"`
}

func (r *GitRef) table() string       { return "git_ref" }
func (r *GitRef) subjectType() string { return "git_ref" }
func (r *GitRef) subjectID() string   { return r.ID }

// Clone returns a copy to mutate, leaving the original as the before image.
func (r *GitRef) Clone() *GitRef {
	clone := *r
	return &clone
}

// NewGitRef returns an unsaved ref.
func NewGitRef(repo, kind, name, sha string) *GitRef {
	return &GitRef{
		ID:        RefID(repo, kind, name),
		RepoID:    repo,
		Name:      name,
		Kind:      kind,
		CommitSHA: sha,
	}
}

// RefWait is what an action is waiting for, when it is waiting for a ref.
//
// Pattern is a glob over the short name and After is an exclusive lower bound
// on the version in it. Both matter: the pattern alone matches the release
// that already shipped, and a bound alone would be satisfied by a patch tag —
// which does not carry the "the previous release finished rolling out"
// implication that makes any of this a useful proxy.
type RefWait struct {
	ActionID string
	RepoID   string
	Kind     string
	Pattern  string
	// After is empty when any name matching the pattern will do.
	After string
}

// Matches reports whether a ref is the one this wait was waiting for.
//
// A pure function of two stored rows, which is the rule every predicate
// follows. Nothing here reads the clock or the network.
func (w RefWait) Matches(ref *GitRef) bool {
	if ref.RepoID != w.RepoID || ref.Kind != w.Kind {
		return false
	}
	if !globMatch(w.Pattern, ref.Name) {
		return false
	}
	// A pre-release is not the release, and a wait that did not ask for one
	// does not want it. The glob cannot express this: `v*.*.0` happens to
	// exclude v1.2.0-rc1 because of where the literal falls, but `v*.*.*` —
	// the natural way to write "any release" — matches it.
	//
	// Ordering alone would not be enough either. Under correct semver
	// v1.2.0-rc1 still sorts above a bound of v1.1.0, so the wait would be
	// satisfied by a release candidate for a release that has not happened.
	if isPrerelease(ref.Name) && !w.wantsPrerelease() {
		return false
	}
	if w.After == "" {
		return true
	}
	return compareRefVersions(ref.Name, w.After) > 0
}

// wantsPrerelease reports whether this wait was written with one in mind.
//
// Asking for v1.2.0-rc* or bounding at v1.2.0-rc1 is an unambiguous statement
// that release candidates are the point, so the exclusion lifts. Anything else
// wants the release.
//
// The pattern is tested for a hyphen rather than parsed, because a pattern is
// not a version: `v1.2.0-rc*` is exactly the thing somebody would write here
// and exactly the thing semver rejects, so asking whether it parses answers
// no for the case this exists to allow.
//
// A hyphen in a scheme semver cannot read — `release-1.5.0` — is a false
// positive that costs nothing: a ref under such a scheme is never a
// pre-release, so this is not reached for it.
func (w RefWait) wantsPrerelease() bool {
	return strings.Contains(lastSegment(w.Pattern), "-") || isPrerelease(w.After)
}

// Describe renders the wait the way a listing should show it.
func (w RefWait) Describe() string {
	if w.After == "" {
		return fmt.Sprintf("%s %s %s", w.RepoID, w.Kind, w.Pattern)
	}
	return fmt.Sprintf("%s %s %s after %s", w.RepoID, w.Kind, w.Pattern, w.After)
}

// LiteralPrefix returns the leading part of the pattern that contains no
// wildcard, which is what GitHub can filter on.
//
// Asking for refs/tags/v narrows a repository with thousands of tags to the
// ones that could possibly match. It is an optimisation and never a filter:
// the pattern is applied again to whatever comes back.
func (w RefWait) LiteralPrefix() string {
	if i := strings.IndexAny(w.Pattern, "*?"); i >= 0 {
		return w.Pattern[:i]
	}
	return w.Pattern
}

// globMatch reports whether name matches a pattern of literals, `*` and `?`.
//
// Not filepath.Match: that treats `/` as a separator and rejects a malformed
// pattern with an error, neither of which is wanted for a ref name. `*` here
// spans anything, including dots, which is what makes v*.*.0 mean what a
// person writing it expects.
func globMatch(pattern, name string) bool {
	// Iterative backtracking rather than recursion: linear in the common case
	// and immune to a pattern of many stars.
	var p, n, star, mark int
	star = -1
	for n < len(name) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == name[n]):
			p++
			n++
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, n
			p++
		case star >= 0:
			// Give the last star one more character and try again.
			p, mark = star+1, mark+1
			n = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// lastSegment is the part of a ref after the final slash, which is where the
// version lives.
//
// A monorepo tags service/s3/v1.107.0, and the directory names a component
// rather than a version — the `3` in `s3` is not a version number, and reading
// the whole string would make it one.
func lastSegment(ref string) string {
	if slash := strings.LastIndex(ref, "/"); slash >= 0 {
		return ref[slash+1:]
	}
	return ref
}

// semverOf renders a ref's version in the form x/mod/semver accepts, or ""
// where it is not a semantic version at all.
//
// The package requires a leading v and rejects a bare 1.2.0, which is a
// common enough way to tag that normalising it is worth the two lines. A
// release-1.5.0 has no reading as semver and falls back to the digits.
func semverOf(ref string) string {
	name := lastSegment(ref)
	if name == "" {
		return ""
	}
	if name[0] != 'v' {
		name = "v" + name
	}
	if !semver.IsValid(name) {
		return ""
	}
	return name
}

// isPrerelease reports whether a ref names a release candidate rather than a
// release: the -rc1 in v1.2.0-rc1.
//
// Only meaningful for semantic versions. A scheme this cannot read has no
// notion of a pre-release, so nothing is excluded — the safe direction, since
// the alternative is silently ignoring the ref somebody is waiting for.
func isPrerelease(ref string) bool {
	version := semverOf(ref)
	return version != "" && semver.Prerelease(version) != ""
}

// compareRefVersions orders two ref names by version.
//
// Semantic versions go through x/mod/semver, which is the only way to get
// v1.2.0-rc1 below v1.2.0: a pre-release sorts *under* its release, and no
// amount of comparing the digits in order produces that — the digits say
// [1 2 0 1] against [1 2 0], which is greater.
//
// Both sides must parse, or neither is used. Mixing two orderings within one
// comparison would make the result depend on which side happened to be
// well-formed, and a wait on release-1.5.0 would compare against v1.6.0 under
// rules only one of them follows.
func compareRefVersions(a, b string) int {
	if x, y := semverOf(a), semverOf(b); x != "" && y != "" {
		return semver.Compare(x, y)
	}
	return compareVersions(versionOf(a), versionOf(b))
}

// versionOf extracts the numbers from a ref name, in order.
//
// v1.5.0, 1.5.0 and release-1.5.0 all yield [1 5 0], which is the point: tag
// naming differs per repository and a rule configured per repository is a
// setting to get wrong. Numbers rather than string comparison because v1.10.0
// sorts below v1.5.0 as text and above it as a version.
//
// Only the last path segment is read, which is what makes directory-prefixed
// tags work. A monorepo tags `service/s3/v1.107.0`, and the `3` in `s3` is
// part of a component's name rather than of a version — taking the whole
// string would read that as [3 1 107 0] and compare it against a bound with a
// different prefix, or none, as though the two were versions of each other. It
// also means the bound may be written either way: `--ref-after v1.106.0` and
// `--ref-after service/s3/v1.106.0` are the same request.
//
// Non-numeric parts are dropped rather than ordered. A pre-release such as
// v1.5.0-rc1 therefore reads as [1 5 0 1] and sorts *above* v1.5.0, which is
// backwards — but a pattern like v*.*.0 does not match it in the first place,
// so the bound never sees one. Ordering pre-releases properly is semver, and
// semver is a per-repository convention this deliberately does not assume.
func versionOf(ref string) []int {
	name := lastSegment(ref)

	var parts []int
	for i := 0; i < len(name); {
		if name[i] < '0' || name[i] > '9' {
			i++
			continue
		}
		j := i
		for j < len(name) && name[j] >= '0' && name[j] <= '9' {
			j++
		}
		// Overflow is not a version; a 20-digit run is something else.
		if v, err := strconv.Atoi(name[i:j]); err == nil {
			parts = append(parts, v)
		}
		i = j
	}
	return parts
}

// compareVersions orders two extracted versions, padding the shorter with
// zeros so that v1.5 and v1.5.0 compare equal.
func compareVersions(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// ObserveRef records a ref GitHub reported, and reports whether it is new.
//
// Insert or update, because a ref moves: a branch gains commits and its head
// changes, while its identity does not. The first sighting is an event — a
// release branch being cut is the news somebody was waiting for — and a moved
// head is a change to an observed column, which the diff logs on its own.
func (t *Tx) ObserveRef(ctx context.Context, ref *GitRef) (bool, error) {
	before, err := t.LoadGitRef(ctx, ref.ID)
	if err == sql.ErrNoRows {
		if err := t.Insert(ctx, ref); err != nil {
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}

	after := before.Clone()
	after.CommitSHA = ref.CommitSHA
	if _, err := t.Update(ctx, before, after); err != nil {
		return false, err
	}
	return false, nil
}

// HasRefs reports whether anything has been observed for a repository and
// kind yet.
//
// What distinguishes a backfill from news. The first poll of a repository
// sees its whole tag history at once — a hundred releases that existed long
// before anybody waited for one — and calling each of those an appearance
// would bury the one that matters under the ninety-nine that do not.
func (s *Store) HasRefs(ctx context.Context, repo, kind string) (bool, error) {
	var found int
	err := s.db.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM git_ref WHERE repo_id = ? AND kind = ?)",
		repo, kind).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("checking for observed refs in %s: %w", repo, err)
	}
	return found == 1, nil
}

// LoadGitRef reads a ref by identifier.
func (t *Tx) LoadGitRef(ctx context.Context, id string) (*GitRef, error) {
	var r GitRef
	if err := t.Load(ctx, &r, id); err != nil {
		return nil, err
	}
	return &r, nil
}

// RefsIn returns the observed refs of one kind in one repository, newest
// first by version and then by name.
func (t *Tx) RefsIn(ctx context.Context, repo, kind string) ([]*GitRef, error) {
	return scanRefs(ctx, t.tx.QueryContext, repo, kind)
}

// ListRefs returns observed refs, optionally narrowed to one repository.
func (s *Store) ListRefs(ctx context.Context, repo string) ([]*GitRef, error) {
	fields, err := fieldsOfStruct(&GitRef{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	query := fmt.Sprintf("SELECT %s FROM git_ref", strings.Join(columns, ", "))
	args := []any{}
	if repo != "" {
		query += " WHERE repo_id = ?"
		args = append(args, repo)
	}
	query += " ORDER BY repo_id, kind, name"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing refs: %w", err)
	}
	defer rows.Close()
	return collectRefs(rows, fields)
}

// queryFunc is the shape both a Tx and a Store expose for reading.
type queryFunc func(ctx context.Context, query string, args ...any) (*sql.Rows, error)

func scanRefs(ctx context.Context, query queryFunc, repo, kind string) ([]*GitRef, error) {
	fields, err := fieldsOfStruct(&GitRef{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	rows, err := query(ctx, fmt.Sprintf(
		"SELECT %s FROM git_ref WHERE repo_id = ? AND kind = ? ORDER BY name",
		strings.Join(columns, ", ")), repo, kind)
	if err != nil {
		return nil, fmt.Errorf("reading refs for %s: %w", repo, err)
	}
	defer rows.Close()
	return collectRefs(rows, fields)
}

func collectRefs(rows *sql.Rows, fields []field) ([]*GitRef, error) {
	var refs []*GitRef
	for rows.Next() {
		var r GitRef
		dest := make([]any, len(fields))
		for i, f := range fields {
			dest[i] = f.pointerOf(&r)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("reading a ref: %w", err)
		}
		refs = append(refs, &r)
	}
	return refs, rows.Err()
}

// SetRefWait records what an action is waiting for, replacing any earlier
// wait on the same action.
//
// Replaces rather than adds: the table is keyed on the action because one
// action waits for one thing, and correcting a mistyped pattern should not
// need the old row deleted first.
func (t *Tx) SetRefWait(ctx context.Context, w RefWait) error {
	if err := ValidateRefKind(w.Kind); err != nil {
		return err
	}
	if w.Pattern == "" {
		return fmt.Errorf("a ref wait needs a pattern, e.g. 'v*.*.0'")
	}
	after := sql.NullString{String: w.After, Valid: w.After != ""}

	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO action_ref_wait (action_id, repo_id, kind, pattern, after, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(action_id) DO UPDATE SET
		  repo_id = excluded.repo_id, kind = excluded.kind,
		  pattern = excluded.pattern, after = excluded.after`,
		w.ActionID, w.RepoID, w.Kind, w.Pattern, after, t.at)
	if err != nil {
		return fmt.Errorf("recording what %s is waiting for: %w", w.ActionID, err)
	}
	return nil
}

// RefWaitFor returns what an action is waiting for, if anything.
func (t *Tx) RefWaitFor(ctx context.Context, actionID string) (*RefWait, error) {
	var w RefWait
	var after sql.NullString
	err := t.tx.QueryRowContext(ctx, `
		SELECT action_id, repo_id, kind, pattern, after
		FROM action_ref_wait WHERE action_id = ?`, actionID).
		Scan(&w.ActionID, &w.RepoID, &w.Kind, &w.Pattern, &after)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading what %s is waiting for: %w", actionID, err)
	}
	w.After = after.String
	return &w, nil
}

// OpenRefWaits returns the waits belonging to actions that are still open.
//
// This is what sync polls. Deriving the poll set from outstanding waits rather
// than from per-repository configuration means the set is exactly right by
// construction: nothing is polled that nothing is waiting for, and a wait
// cannot be written against a repository sync forgot to watch.
func (s *Store) OpenRefWaits(ctx context.Context) ([]RefWait, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.action_id, w.repo_id, w.kind, w.pattern, w.after
		FROM action_ref_wait w
		JOIN action a ON a.id = w.action_id
		WHERE a.closed_at IS NULL
		ORDER BY w.repo_id, w.kind, w.pattern`)
	if err != nil {
		return nil, fmt.Errorf("reading outstanding ref waits: %w", err)
	}
	defer rows.Close()

	var waits []RefWait
	for rows.Next() {
		var w RefWait
		var after sql.NullString
		if err := rows.Scan(&w.ActionID, &w.RepoID, &w.Kind, &w.Pattern, &after); err != nil {
			return nil, fmt.Errorf("reading an outstanding ref wait: %w", err)
		}
		w.After = after.String
		waits = append(waits, w)
	}
	return waits, rows.Err()
}
