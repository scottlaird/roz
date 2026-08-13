package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// Version components a relative ref spec can bump.
const (
	ComponentMajor = "major"
	ComponentMinor = "minor"
	ComponentPatch = "patch"
)

// RefComponents is the vocabulary, for an error that names the alternatives.
var RefComponents = []string{ComponentMajor, ComponentMinor, ComponentPatch}

// RelativeRef is a release gate written against wherever a repository has got
// to, rather than against a version somebody knew when they wrote it.
//
// A pipeline is defined once and instantiated for every pull request, so an
// absolute version in one would be wrong for every release after the first.
// `minor+2` says "two minors on from here", which stays true.
type RelativeRef struct {
	// PathPrefix selects a series in a monorepo, as it does for a wait.
	PathPrefix string
	// Component is which part of the version to bump.
	Component string
	// Offset is by how much, and is at least one — an offset of nought would
	// resolve to a version that already exists, and the gate would open the
	// moment it was written.
	Offset int
}

// relativeOperator is the only one a release gate takes.
//
// It is fixed rather than chosen. `>=` is what the gate means — the release
// line has opened — and the alternatives have no reading: waiting for at most
// two minors on, or for a version compatible with a rule, is not a thing
// anybody wants. Requiring it is what keeps a gate looking like the wait it
// becomes.
const relativeOperator = ">="

// ParseRelativeRef reads `[prefix/]>=component+n`.
//
// The same shape a wait takes, so `api/>=minor+2` selects the api series and
// counts within it, beside `api/>=3.6` which names a version outright. The
// final slash divides path from rule, which is unambiguous because no
// component name contains one.
//
// The two forms cannot be confused: `>=minor+2` is not a parseable version
// constraint and `>=3.6` is, so which one a spec is never depends on context.
func ParseRelativeRef(spec string) (RelativeRef, error) {
	prefix, rest, err := ParseRefSpec(spec)
	if err != nil {
		return RelativeRef{}, err
	}

	rule, ok := strings.CutPrefix(rest, relativeOperator)
	if !ok {
		return RelativeRef{}, fmt.Errorf(
			"%q is not a release gate: write it as %scomponent+n, e.g. %sminor+2",
			spec, relativeOperator, relativeOperator)
	}

	component, offset, found := strings.Cut(strings.TrimSpace(rule), "+")
	if !found {
		return RelativeRef{}, fmt.Errorf(
			"%q is not a release gate: write it as %scomponent+n, e.g. %sminor+2",
			spec, relativeOperator, relativeOperator)
	}
	component = strings.TrimSpace(component)
	if !validComponent(component) {
		return RelativeRef{}, fmt.Errorf("%q is not a version component: use %s",
			component, strings.Join(RefComponents, ", "))
	}
	n, err := strconv.Atoi(strings.TrimSpace(offset))
	if err != nil {
		return RelativeRef{}, fmt.Errorf("%q is not a number of %ss to skip", offset, component)
	}
	if n < 1 {
		return RelativeRef{}, fmt.Errorf(
			"%s%s+%d would resolve to a version that already exists; skip at least one",
			relativeOperator, component, n)
	}
	return RelativeRef{PathPrefix: prefix, Component: component, Offset: n}, nil
}

func validComponent(c string) bool {
	for _, known := range RefComponents {
		if c == known {
			return true
		}
	}
	return false
}

// versionRuleOpeners are the characters a version rule starts with.
//
// Not a vocabulary anybody chose: they are semver's operators, and two of
// them — ^ and ~ — git refuses in a ref name outright.
const versionRuleOpeners = "><=^~"

// LooksLikeVersionRule reports whether a matcher was meant as a rule about
// versions rather than as the name of a ref.
//
// A matcher's two readings are otherwise told apart by whether it parses,
// which is right for everything well-formed and wrong for a typo. `>=minr+1`
// is neither a constraint nor a gate, so the parse-or-literal rule reads it as
// a name and waits for a tag called `>=minr+1` — for ever, since nothing is
// called that. This is what lets the mistake be refused instead.
//
// The shape is the signal. A rule opens with an operator, or with the name of
// the component it counts; a ref whose name opens either way does not exist,
// and the well-formed expressions have all parsed by the time this is asked.
func LooksLikeVersionRule(matcher string) bool {
	if matcher == "" {
		return false
	}
	if strings.ContainsRune(versionRuleOpeners, rune(matcher[0])) {
		return true
	}
	// The operator left off: `minor+2` is how the rule reads aloud, and is
	// what somebody writes who has seen one in a pipeline and not the `>=`.
	for _, component := range RefComponents {
		if strings.HasPrefix(matcher, component+"+") {
			return true
		}
	}
	return false
}

// String renders the spec back to how it was written.
func (r RelativeRef) String() string {
	spec := fmt.Sprintf("%s%s+%d", relativeOperator, r.Component, r.Offset)
	if r.PathPrefix == "" {
		return spec
	}
	return r.PathPrefix + "/" + spec
}

// Resolve turns the relative gate into the constraint a wait can hold, from
// the highest version already tagged in its series.
//
// Everything below the bumped component is zeroed, so `minor+2` against
// v1.7.5 is 1.9.0 rather than 1.9.5 — a release is the whole of its minor
// line, and the patch somebody happened to be on says nothing about where the
// next line starts.
//
// The result is `>=` rather than `=`, so a line that opens at 1.9.1 because
// 1.9.0 was pulled still satisfies it. Waiting for an exact version means
// waiting for a particular release to go well, which is not what a gate is for.
//
// Returns ok=false when the series holds no version to count from. That is not
// an error: a repository whose first release has not happened cannot answer
// this yet, and the caller tries again on the next poll.
func (r RelativeRef) Resolve(refs []*GitRef) (RefWait, bool) {
	var highest *semver.Version
	for _, ref := range refs {
		prefix, name := splitRefPath(ref.Name)
		if prefix != r.PathPrefix {
			continue
		}
		// Lenient about a leading v, so v1.7.5 and 1.7.5 both count.
		parsed, err := semver.NewVersion(name)
		if err != nil {
			continue
		}
		// A pre-release says a line is being prepared, not that it has
		// happened, so it is not something to count from.
		if parsed.Prerelease() != "" {
			continue
		}
		if highest == nil || parsed.GreaterThan(highest) {
			highest = parsed
		}
	}
	if highest == nil {
		return RefWait{}, false
	}

	major, minor, patch := highest.Major(), highest.Minor(), highest.Patch()
	switch r.Component {
	case ComponentMajor:
		major, minor, patch = major+uint64(r.Offset), 0, 0
	case ComponentMinor:
		minor, patch = minor+uint64(r.Offset), 0
	case ComponentPatch:
		patch += uint64(r.Offset)
	}

	return RefWait{
		PathPrefix: r.PathPrefix,
		Matcher:    fmt.Sprintf(">=%d.%d.%d", major, minor, patch),
	}, true
}

// PendingRef is a step that instantiated before its version could be worked
// out.
type PendingRef struct {
	ActionID string
	RepoID   string
	Kind     string
	Spec     string
}

// Relative parses the spec back.
func (p PendingRef) Relative() (RelativeRef, error) { return ParseRelativeRef(p.Spec) }

// Series is the path this gate counts within, which is what sync asks GitHub
// for. Empty is the top-level one.
func (p PendingRef) Series() string {
	r, err := p.Relative()
	if err != nil {
		return ""
	}
	return r.PathPrefix
}

// AddPendingRef records that an action is waiting for a release whose number
// is not known yet.
//
// Any wait already on the action goes. An action waits for one thing, and a
// gate written over a wait means the wait was wrong — leaving it would let the
// old one fire while the gate was still being worked out, closing the action
// on a release nobody is waiting for any more.
func (t *Tx) AddPendingRef(ctx context.Context, p PendingRef) error {
	if err := ValidateRefKind(p.Kind); err != nil {
		return err
	}
	if _, err := ParseRelativeRef(p.Spec); err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx,
		"DELETE FROM action_ref_wait WHERE action_id = ?", p.ActionID); err != nil {
		return fmt.Errorf("clearing what %s used to wait for: %w", p.ActionID, err)
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO action_ref_pending (action_id, repo_id, kind, spec, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(action_id) DO UPDATE SET
		  repo_id = excluded.repo_id, kind = excluded.kind, spec = excluded.spec`,
		p.ActionID, p.RepoID, p.Kind, p.Spec, t.at)
	if err != nil {
		return fmt.Errorf("recording what %s will wait for: %w", p.ActionID, err)
	}
	return nil
}

// clearPendingRef drops an action's unresolved gate, if it has one.
//
// Idempotent, and the two places that call it both mean the same thing by it:
// this action's version is settled now, by whatever route.
func (t *Tx) clearPendingRef(ctx context.Context, actionID string) error {
	if _, err := t.tx.ExecContext(ctx,
		"DELETE FROM action_ref_pending WHERE action_id = ?", actionID); err != nil {
		return fmt.Errorf("clearing the release gate for %s: %w", actionID, err)
	}
	return nil
}

// PendingRefs returns the gates still waiting to be worked out, for actions
// that are still open.
//
// This is also what makes sync poll the repository: a gate cannot be resolved
// without reading the tags, and nothing else would have asked for them.
func (s *Store) PendingRefs(ctx context.Context) ([]PendingRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.action_id, p.repo_id, p.kind, p.spec
		FROM action_ref_pending p
		JOIN action a ON a.id = p.action_id
		WHERE a.closed_at IS NULL
		ORDER BY p.repo_id, p.kind, p.action_id`)
	if err != nil {
		return nil, fmt.Errorf("reading unresolved release gates: %w", err)
	}
	defer rows.Close()

	var pending []PendingRef
	for rows.Next() {
		var p PendingRef
		if err := rows.Scan(&p.ActionID, &p.RepoID, &p.Kind, &p.Spec); err != nil {
			return nil, fmt.Errorf("reading an unresolved release gate: %w", err)
		}
		pending = append(pending, p)
	}
	return pending, rows.Err()
}

// Resolved is one gate that has just been given a number.
type Resolved struct {
	ActionID string
	Spec     string
	Wait     RefWait
}

// ResolvePendingRef works out what a gate waits for and turns it into a real
// wait, reporting nothing when the series has no release to count from yet.
//
// One transaction: the wait appearing and the gate disappearing are the same
// act, and a crash between them would leave an action waiting for two things
// or for nothing.
func (s *Store) ResolvePendingRef(ctx context.Context, p PendingRef) (*Resolved, error) {
	relative, err := p.Relative()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.ActionID, err)
	}

	tx, err := s.Begin(ctx, ActorPredicate)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	refs, err := tx.RefsIn(ctx, p.RepoID, p.Kind)
	if err != nil {
		return nil, err
	}
	wait, ok := relative.Resolve(refs)
	if !ok {
		return nil, nil
	}
	wait.ActionID, wait.RepoID, wait.Kind = p.ActionID, p.RepoID, p.Kind

	// SetRefWait clears the gate as part of writing the wait, which is the
	// same statement this used to make on its own. Said again here because
	// resolving *is* the gate disappearing, and a reader should not have to
	// know that the other call does it.
	if err := tx.SetRefWait(ctx, wait); err != nil {
		return nil, err
	}
	if err := tx.clearPendingRef(ctx, p.ActionID); err != nil {
		return nil, err
	}

	action, err := tx.LoadAction(ctx, p.ActionID)
	if err != nil {
		return nil, err
	}

	// The title carries the rule until the rule has an answer. Once it has
	// one, the queue should say what is actually being waited for rather than
	// how it was worked out — ">=2.99.0" is something to check against the
	// repository, where ">=minor+2" is something to work out again.
	//
	// Only where the title still says what instantiation wrote. A title
	// somebody has since edited is theirs, and a rule that no longer appears
	// in it is not this to rewrite.
	if strings.Contains(action.Title, p.Spec) {
		retitled := action.Clone()
		retitled.Title = strings.Replace(action.Title, p.Spec, wait.Matcher, 1)
		if _, err := tx.Update(ctx, action, retitled); err != nil {
			return nil, err
		}
	}

	// Worth a line in the log either way: what an action waits for was decided
	// by roz rather than written by anybody, and this is the only record of
	// what it counted from.
	if err := tx.emit(ctx, action, event{
		kind: eventChanged, field: "waits_for", oldValue: p.Spec, newValue: wait.Spec(),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Resolved{ActionID: p.ActionID, Spec: p.Spec, Wait: wait}, nil
}
