package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
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
// Written as one expression and stored as its two halves. `api/>=3.6` is the
// api series at 3.6 or later; `>=1.2` is the top-level one.
type RefWait struct {
	ActionID string
	RepoID   string
	Kind     string
	// PathPrefix is everything before the final slash — "api", "service/s3",
	// or "" for a top-level ref. Compared for equality, never across.
	PathPrefix string
	// Matcher is everything after it: a semver constraint, or a literal name
	// or glob where no version scheme applies.
	Matcher string
}

// ParseRefSpec splits `[prefix/]matcher` and checks that the result means
// something.
//
// The final slash divides them, which is unambiguous because no semver
// constraint contains one: `api/>=3.6` is the api series, `service/s3/>=1.107`
// a nested one, `>=1.2` the top level. A colon was the alternative and is more
// explicit, at the cost of not looking like the tag it selects.
func ParseRefSpec(spec string) (prefix, matcher string, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", fmt.Errorf("a ref wait needs something to wait for, e.g. '>=1.2' or 'api/>=3.6'")
	}
	if slash := strings.LastIndex(spec, "/"); slash >= 0 {
		prefix, matcher = spec[:slash], spec[slash+1:]
	} else {
		matcher = spec
	}
	if matcher == "" {
		return "", "", fmt.Errorf("%q has a path but nothing to match: add a constraint, e.g. %q",
			spec, prefix+"/>=1.0")
	}
	return prefix, matcher, nil
}

// Spec renders the wait back to the expression it was written as.
func (w RefWait) Spec() string {
	if w.PathPrefix == "" {
		return w.Matcher
	}
	return w.PathPrefix + "/" + w.Matcher
}

// Constraint returns the semver constraint the matcher expresses, and whether
// it is one at all.
//
// A matcher that does not parse is a literal name or glob. The two forms
// cannot be confused: `release-1.5` is not a constraint, while `>=1.2`, `^1.2`,
// `1.2.x` and `*` all are.
func (w RefWait) Constraint() (*semver.Constraints, bool) {
	c, err := semver.NewConstraint(w.Matcher)
	if err != nil {
		return nil, false
	}
	return c, true
}

// Validate refuses a wait that could never match anything.
func (w RefWait) Validate() error {
	if err := ValidateRefKind(w.Kind); err != nil {
		return err
	}
	if w.Matcher == "" {
		return fmt.Errorf("a ref wait needs something to wait for, e.g. '>=1.2'")
	}
	return nil
}

// Matches reports whether a ref is the one this wait was waiting for.
//
// A pure function of two stored rows, which is the rule every predicate
// follows. Nothing here reads the clock or the network.
func (w RefWait) Matches(ref *GitRef) bool {
	if ref.RepoID != w.RepoID || ref.Kind != w.Kind {
		return false
	}

	// The path prefix must match exactly. A repository's `v1.2.3` and its
	// `api/v3.4.5` are separate series that happen to share a repository, and
	// their versions mean nothing to each other — so a wait for the top-level
	// series is never satisfied by an api release, whatever the numbers say.
	prefix, name := splitRefPath(ref.Name)
	if prefix != w.PathPrefix {
		return false
	}

	constraint, isConstraint := w.Constraint()
	if !isConstraint {
		// A literal name or glob, for a ref no version scheme describes: the
		// release-1.5 branch being cut.
		return globMatch(w.Matcher, name)
	}

	version, err := semver.NewVersion(name)
	if err != nil {
		// Not a version, so a version constraint has nothing to say about it.
		return false
	}
	// Check applies the published rule for pre-releases: >=1.2 does not match
	// 1.3.0-rc1, and >=1.2.0-0 does. That is a specification rather than a
	// local invention, which is most of the argument for using constraints
	// at all.
	return constraint.Check(version)
}

// Describe renders the wait the way a listing should show it.
func (w RefWait) Describe() string {
	return fmt.Sprintf("%s %s %s", w.RepoID, w.Kind, w.Spec())
}

// PollPrefix is what GitHub is asked to filter on.
//
// The path prefix, which narrows a monorepo's thousands of tags to the one
// component in question. An optimisation and never a filter: whatever comes
// back is matched again here.
//
// A top-level wait yields "", so a busy monorepo returns its most recent tags
// across every component and the top-level ones may not be among them. That is
// a real limit, and the reason polling targets belong on the repository —
// scottlaird/roz#128 — rather than being squeezed out of a wait.
func (w RefWait) PollPrefix() string {
	if w.PathPrefix == "" {
		return ""
	}
	return w.PathPrefix + "/"
}

// splitRefPath divides a ref name into its directory and its last segment.
//
// A monorepo tags service/s3/v1.107.0: the directory names a component and the
// last segment is the version. Reading the whole string as a version would
// make the `3` in `s3` one.
func splitRefPath(name string) (prefix, last string) {
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		return name[:slash], name[slash+1:]
	}
	return "", name
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
	if err := w.Validate(); err != nil {
		return err
	}

	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO action_ref_wait (action_id, repo_id, kind, path_prefix, matcher, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(action_id) DO UPDATE SET
		  repo_id = excluded.repo_id, kind = excluded.kind,
		  path_prefix = excluded.path_prefix, matcher = excluded.matcher`,
		w.ActionID, w.RepoID, w.Kind, w.PathPrefix, w.Matcher, t.at)
	if err != nil {
		return fmt.Errorf("recording what %s is waiting for: %w", w.ActionID, err)
	}
	return nil
}

// RefWaitFor returns what an action is waiting for, if anything.
func (t *Tx) RefWaitFor(ctx context.Context, actionID string) (*RefWait, error) {
	var w RefWait
	err := t.tx.QueryRowContext(ctx, `
		SELECT action_id, repo_id, kind, path_prefix, matcher
		FROM action_ref_wait WHERE action_id = ?`, actionID).
		Scan(&w.ActionID, &w.RepoID, &w.Kind, &w.PathPrefix, &w.Matcher)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading what %s is waiting for: %w", actionID, err)
	}
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
		SELECT w.action_id, w.repo_id, w.kind, w.path_prefix, w.matcher
		FROM action_ref_wait w
		JOIN action a ON a.id = w.action_id
		WHERE a.closed_at IS NULL
		ORDER BY w.repo_id, w.kind, w.path_prefix, w.matcher`)
	if err != nil {
		return nil, fmt.Errorf("reading outstanding ref waits: %w", err)
	}
	defer rows.Close()

	var waits []RefWait
	for rows.Next() {
		var w RefWait
		if err := rows.Scan(&w.ActionID, &w.RepoID, &w.Kind, &w.PathPrefix, &w.Matcher); err != nil {
			return nil, fmt.Errorf("reading an outstanding ref wait: %w", err)
		}
		waits = append(waits, w)
	}
	return waits, rows.Err()
}
