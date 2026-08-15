package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Seeded pipelines. A database may have gained others; nothing in the code
// may assume these are all of them.
const (
	PipelineReview = "review"
	PipelineDirect = "direct"
)

// Pipeline is what closing a verb instantiates: the actions that follow a
// piece of work, in order.
//
// It is data rather than code so that a chain can differ by repository
// without a deploy. The limit matches the one actionverb draws around
// predicates: a pipeline names verbs, and can say nothing about a verb the
// vocabulary does not already say.
type Pipeline struct {
	// N orders the table. The lowest-numbered active pipeline is what a
	// newly tracked repository takes, so the order is a statement about which
	// is usual.
	N           int64  `db:"n" kind:"identity"`
	Name        string `db:"name" kind:"identity"`
	Label       string `db:"label"`
	Active      bool   `db:"active"`
	Description string `db:"description"`

	// Steps are what the pipeline instantiates, in order. They live in
	// pipeline_step and are filled in by the loaders, which is why this
	// carries no db tag.
	Steps []PipelineStep
}

// PipelineStep is one step: a verb, and what it waits for where the verb needs
// telling.
//
// Spec is opaque to the pipeline. What it means belongs to the verb — a
// release gate reads it as a version to wait for — which is the same limit
// actionverb draws around predicates: a name, never behaviour.
type PipelineStep struct {
	Verb string
	Spec string
}

// String renders a step the way it is written on the command line.
func (s PipelineStep) String() string {
	if s.Spec == "" {
		return s.Verb
	}
	return s.Verb + "(" + s.Spec + ")"
}

// ParseStep reads `verb` or `verb(spec)`.
//
// Brackets rather than a separator, because a spec is full of operators —
// `>=`, `/`, `+` — and a delimiter is what says where the verb stopped and the
// argument began. They also leave somewhere for a second argument to go, which
// a separator does not: `verb:a:b` is ambiguous the moment anything wants two.
func ParseStep(text string) (PipelineStep, error) {
	text = strings.TrimSpace(text)

	open := strings.Index(text, "(")
	if open < 0 {
		if strings.Contains(text, ")") {
			return PipelineStep{}, fmt.Errorf("%q closes a bracket it never opened", text)
		}
		if text == "" {
			return PipelineStep{}, fmt.Errorf("a step needs a verb")
		}
		return PipelineStep{Verb: text}, nil
	}
	if !strings.HasSuffix(text, ")") {
		return PipelineStep{}, fmt.Errorf("%q opens a bracket it never closes", text)
	}

	verb := strings.TrimSpace(text[:open])
	spec := strings.TrimSpace(text[open+1 : len(text)-1])
	if verb == "" {
		return PipelineStep{}, fmt.Errorf("%q has no verb", text)
	}
	if spec == "" {
		return PipelineStep{}, fmt.Errorf("%q has empty brackets; drop them or say what goes in", text)
	}
	return PipelineStep{Verb: verb, Spec: spec}, nil
}

// SplitSteps divides a chain written as one string, on the commas that are not
// inside a step's brackets.
//
// A plain split would break `wait_ref(>=1.2, <2.0)` into two broken steps.
// Nothing writes a spec with a comma in it today, and the brackets are what
// make it possible to later.
func SplitSteps(text string) []string {
	var steps []string
	depth, start := 0, 0
	for i, r := range text {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				steps = append(steps, text[start:i])
				start = i + 1
			}
		}
	}
	return append(steps, text[start:])
}

// StepsText renders a chain for a message.
func StepsText(steps []PipelineStep) string {
	parts := make([]string, len(steps))
	for i, step := range steps {
		parts[i] = step.String()
	}
	return strings.Join(parts, " → ")
}

func (p *Pipeline) table() string       { return "action_pipeline" }
func (p *Pipeline) subjectType() string { return "action_pipeline" }
func (p *Pipeline) subjectID() string   { return p.Name }

// extraJSON adds the steps, which are not columns of action_pipeline.
func (p *Pipeline) extraJSON() map[string]any {
	steps := make([]string, 0, len(p.Steps))
	for _, step := range p.Steps {
		steps = append(steps, step.String())
	}
	return map[string]any{"steps": steps}
}

// LoadPipeline reads one pipeline and its steps, returning sql.ErrNoRows if
// there is no such pipeline.
func (t *Tx) LoadPipeline(ctx context.Context, name string) (*Pipeline, error) {
	pipelines, err := readPipelines(ctx, t.tx, Sort{}, "WHERE name = ?", name)
	if err != nil {
		return nil, err
	}
	if len(pipelines) == 0 {
		return nil, sql.ErrNoRows
	}
	return pipelines[0], nil
}

// ListPipelines returns the pipelines with their steps, in order.
//
// activeOnly drops retired ones. Like verbs they are deactivated rather than
// deleted, since a repository may still name one.
func (s *Store) ListPipelines(ctx context.Context, activeOnly bool, sort Sort) ([]*Pipeline, error) {
	where := ""
	if activeOnly {
		where = "WHERE active = 1"
	}
	return readPipelines(ctx, s.db, sort, where)
}

// DefaultPipeline returns the lowest-numbered active pipeline, which is what
// a repository takes when nobody says otherwise.
//
// It returns sql.ErrNoRows if every pipeline has been retired. That is a
// database someone has emptied deliberately, and guessing on its behalf would
// be worse than saying so.
func (s *Store) DefaultPipeline(ctx context.Context) (*Pipeline, error) {
	pipelines, err := readPipelines(ctx, s.db, Sort{},
		"WHERE n = (SELECT min(n) FROM action_pipeline WHERE active = 1)")
	if err != nil {
		return nil, err
	}
	if len(pipelines) == 0 {
		return nil, sql.ErrNoRows
	}
	return pipelines[0], nil
}

// querier is the part of *sql.DB and *sql.Tx that reading needs, so a
// pipeline can be read either inside a unit of work or outside one.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// readPipelines runs one query for the pipelines and a second for every
// step belonging to them, rather than a join returning a row per step.
func readPipelines(ctx context.Context, q querier, sort Sort, where string, args ...any) ([]*Pipeline, error) {
	fields, err := fieldsOfStruct(&Pipeline{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	order := sort.SQL("")
	if order == "" {
		order = "n"
	}
	query := fmt.Sprintf("SELECT %s FROM action_pipeline %s ORDER BY "+order,
		strings.Join(columns, ", "), where)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading pipelines: %w", err)
	}
	defer rows.Close()

	var pipelines []*Pipeline
	byName := map[string]*Pipeline{}
	for rows.Next() {
		var p Pipeline
		dest := make([]any, len(fields))
		for i, f := range fields {
			dest[i] = f.pointerOf(&p)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("reading pipelines: %w", err)
		}
		pipelines = append(pipelines, &p)
		byName[p.Name] = &p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading pipelines: %w", err)
	}
	if len(pipelines) == 0 {
		return nil, nil
	}
	return pipelines, readSteps(ctx, q, byName)
}

func readSteps(ctx context.Context, q querier, byName map[string]*Pipeline) error {
	rows, err := q.QueryContext(ctx,
		"SELECT pipeline, verb, coalesce(spec, '') FROM pipeline_step ORDER BY pipeline, position")
	if err != nil {
		return fmt.Errorf("reading pipeline steps: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		var step PipelineStep
		if err := rows.Scan(&name, &step.Verb, &step.Spec); err != nil {
			return fmt.Errorf("reading pipeline steps: %w", err)
		}
		if p, ok := byName[name]; ok {
			p.Steps = append(p.Steps, step)
		}
	}
	return rows.Err()
}

// keyColumn: a pipeline is keyed on its name, not on a surrogate. The table's
// primary key is n, which orders it rather than identifying it — renumbering
// one must not look like a different pipeline.
func (p *Pipeline) keyColumn() string { return "name" }

// NewPipeline returns an unsaved pipeline. N is assigned on insert, since
// where it lands in the order is a decision the caller makes separately.
func NewPipeline(name, label string) *Pipeline {
	return &Pipeline{Name: name, Label: label, Active: true}
}

// Clone returns a copy to mutate, leaving the original as the before image.
func (p *Pipeline) Clone() *Pipeline {
	clone := *p
	clone.Steps = append([]PipelineStep(nil), p.Steps...)
	return &clone
}

// ValidatePipelineName refuses a name that would not work as one.
//
// It is an identifier: repositories name it, `roz repo set --pipeline` takes
// it, and it is what the log records. A name with spaces or commas in it would
// survive the database and be miserable everywhere else, commas most of all,
// since that is how a list of steps is written.
func ValidatePipelineName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("a pipeline needs a name")
	case strings.ContainsAny(name, " \t,"):
		return fmt.Errorf("pipeline name %q cannot contain spaces or commas", name)
	}
	return nil
}

// PipelineStepError says why a verb cannot be a step.
type PipelineStepError struct {
	Verb   string
	Reason string
}

func (e *PipelineStepError) Error() string {
	return fmt.Sprintf("%q cannot be a pipeline step: %s", e.Verb, e.Reason)
}

// CheckPipelineSteps refuses a chain that could not run.
//
// A pipeline is the predicate chain that *follows* a human action, so every
// step has to be able to close on its own. A human-closed verb in the middle
// of one is a chain that stops until somebody notices, which is the thing
// pipelines exist to avoid.
//
// A verb needing a ref wait is refused for the same reason from the other
// side: instantiation has the pull request to hand and nothing else, so a
// `wait_ref` step would be created with nothing to wait for and would block
// everything behind it for ever. Giving a step somewhere to carry that is
// scottlaird/roz#127.
//
// Retired verbs are refused because new actions may not use them, which is
// what retiring a verb means. An existing pipeline naming one keeps working:
// this is a check on what is written, not on what was.
func (t *Tx) CheckPipelineSteps(ctx context.Context, steps []PipelineStep) error {
	for _, step := range steps {
		v, err := t.LoadVerb(ctx, step.Verb)
		if err == sql.ErrNoRows {
			return &PipelineStepError{Verb: step.Verb, Reason: "there is no such verb"}
		}
		if err != nil {
			return err
		}
		switch {
		case !v.Active:
			return &PipelineStepError{Verb: step.Verb, Reason: "it is retired"}
		case v.Closes != ClosesPredicate:
			return &PipelineStepError{Verb: step.Verb,
				Reason: "it closes when a person says so, and a pipeline is what follows a person's work"}
		case v.RequiresRef && step.Spec == "":
			return &PipelineStepError{Verb: step.Verb,
				Reason: "it waits for a ref, so the step has to say which: " + step.Verb + "(>=minor+2)"}
		case !v.RequiresRef && step.Spec != "":
			return &PipelineStepError{Verb: step.Verb,
				Reason: "it takes no spec, and was given " + step.Spec}
		}
		if step.Spec == "" {
			continue
		}
		// An absolute version is refused rather than accepted, even though the
		// syntax now admits one. A pipeline is written once and instantiated
		// for every pull request, so a fixed version is the same gate forever:
		// right for the release it names and wrong for every one after it.
		// Saying so is more use than letting it through.
		if _, _, err := ParseRefSpec(step.Spec); err == nil {
			if w := (RefWait{Matcher: matcherOf(step.Spec)}); w.Matcher != "" {
				if _, isAbsolute := w.Constraint(); isAbsolute {
					return &PipelineStepError{Verb: step.Verb, Reason: fmt.Sprintf(
						"%q names a version, which would be the same gate for every pull request; "+
							"write it relative, e.g. %s", step.Spec, relativeExample(step.Spec))}
				}
			}
		}
		if _, err := ParseRelativeRef(step.Spec); err != nil {
			return &PipelineStepError{Verb: step.Verb, Reason: err.Error()}
		}
	}
	return nil
}

// SetSteps replaces a pipeline's steps, logging the change against it.
//
// Replaces rather than edits: a chain is short and ordered, and "the steps are
// now these" is the only edit anybody makes to one. Positions are rewritten
// from scratch, so nothing has to renumber around an insertion.
//
// Chains already instantiated are untouched by construction — steps are copied
// into actions when a chain starts, so nothing reads this table again.
func (t *Tx) SetSteps(ctx context.Context, p *Pipeline, steps []PipelineStep) error {
	if err := t.CheckPipelineSteps(ctx, steps); err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx,
		"DELETE FROM pipeline_step WHERE pipeline = ?", p.Name); err != nil {
		return fmt.Errorf("clearing the steps of %s: %w", p.Name, err)
	}
	for i, step := range steps {
		var spec any
		if step.Spec != "" {
			spec = step.Spec
		}
		if _, err := t.tx.ExecContext(ctx,
			"INSERT INTO pipeline_step (pipeline, position, verb, spec) VALUES (?, ?, ?, ?)",
			p.Name, i+1, step.Verb, spec); err != nil {
			return fmt.Errorf("writing step %d of %s: %w", i+1, p.Name, err)
		}
	}

	// Steps are rows rather than a column, so the diff that Update produces
	// cannot see them. Logged here instead, as one change to the chain rather
	// than one per row: what a reader wants is what it runs now.
	before, after := StepsText(p.Steps), StepsText(steps)
	if before != after {
		if err := t.emit(ctx, p, event{
			kind: eventChanged, field: "steps", oldValue: before, newValue: after,
		}); err != nil {
			return err
		}
	}
	p.Steps = steps
	return nil
}

// PipelineOrder is where a new pipeline lands, which decides whether newly
// tracked repositories take it.
const (
	// OrderLast puts it after the others: available, and not the default.
	OrderLast = "last"
	// OrderFirst puts it in front, making it what a newly tracked repository
	// takes. Existing repositories are unaffected: they resolved their
	// pipeline when they were tracked.
	OrderFirst = "first"
)

// PipelineOrders is the vocabulary, for an error that names the alternatives.
var PipelineOrders = []string{OrderLast, OrderFirst}

// InsertPipeline writes a new pipeline at one end of the order.
//
// N is assigned here rather than by the caller because it is load-bearing and
// easy to get wrong: the lowest-numbered active pipeline is what every newly
// tracked repository takes, so a number chosen by hand silently changes that.
// Naming an end says the intent and lets this work out the number.
//
// Gaps and negative numbers are fine. N orders the table and is not a rank;
// nothing counts it, and renumbering the others to keep it dense would rewrite
// rows that did not change.
func (t *Tx) InsertPipeline(ctx context.Context, p *Pipeline, order string) error {
	if err := ValidatePipelineName(p.Name); err != nil {
		return err
	}
	if p.Label == "" {
		p.Label = p.Name
	}

	var n sql.NullInt64
	column := "max(n) + 1"
	if order == OrderFirst {
		column = "min(n) - 1"
	}
	if err := t.tx.QueryRowContext(ctx,
		"SELECT "+column+" FROM action_pipeline").Scan(&n); err != nil {
		return fmt.Errorf("finding where %s goes: %w", p.Name, err)
	}
	p.N = 1
	if n.Valid {
		p.N = n.Int64
	}
	return t.Insert(ctx, p)
}

// PipelineUsers returns the repositories naming a pipeline.
//
// Retiring one does not strand them — a repository's pipeline is read by name
// and retirement only removes it from what a *new* repository would take — but
// it is worth saying how many are still on it, since the answer is usually the
// reason somebody is retiring it.
func (s *Store) PipelineUsers(ctx context.Context, name string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id FROM github_repo WHERE pipeline = ? ORDER BY id", name)
	if err != nil {
		return nil, fmt.Errorf("finding what uses %s: %w", name, err)
	}
	defer rows.Close()

	var repos []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("finding what uses %s: %w", name, err)
		}
		repos = append(repos, id)
	}
	return repos, rows.Err()
}

// matcherOf is the rule half of a ref spec, or empty when it has none.
func matcherOf(spec string) string {
	_, matcher, err := ParseRefSpec(spec)
	if err != nil {
		return ""
	}
	return matcher
}

// relativeExample rewrites an absolute spec into the relative form it should
// probably have been, keeping the series so the suggestion is usable as typed.
func relativeExample(spec string) string {
	prefix, _, err := ParseRefSpec(spec)
	if err != nil || prefix == "" {
		return relativeOperator + "minor+2"
	}
	return prefix + "/" + relativeOperator + "minor+2"
}
