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

	// Steps are the verbs the pipeline instantiates, in order. They live in
	// pipeline_step and are filled in by the loaders, which is why this
	// carries no db tag.
	Steps []string
}

func (p *Pipeline) table() string       { return "action_pipeline" }
func (p *Pipeline) subjectType() string { return "action_pipeline" }
func (p *Pipeline) subjectID() string   { return p.Name }

// extraJSON adds the steps, which are not columns of action_pipeline.
func (p *Pipeline) extraJSON() map[string]any {
	steps := p.Steps
	if steps == nil {
		steps = []string{}
	}
	return map[string]any{"steps": steps}
}

// LoadPipeline reads one pipeline and its steps, returning sql.ErrNoRows if
// there is no such pipeline.
func (t *Tx) LoadPipeline(ctx context.Context, name string) (*Pipeline, error) {
	pipelines, err := readPipelines(ctx, t.tx, "WHERE name = ?", name)
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
func (s *Store) ListPipelines(ctx context.Context, activeOnly bool) ([]*Pipeline, error) {
	where := ""
	if activeOnly {
		where = "WHERE active = 1"
	}
	return readPipelines(ctx, s.db, where)
}

// DefaultPipeline returns the lowest-numbered active pipeline, which is what
// a repository takes when nobody says otherwise.
//
// It returns sql.ErrNoRows if every pipeline has been retired. That is a
// database someone has emptied deliberately, and guessing on its behalf would
// be worse than saying so.
func (s *Store) DefaultPipeline(ctx context.Context) (*Pipeline, error) {
	pipelines, err := readPipelines(ctx, s.db,
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
func readPipelines(ctx context.Context, q querier, where string, args ...any) ([]*Pipeline, error) {
	fields, err := fieldsOfStruct(&Pipeline{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	query := fmt.Sprintf("SELECT %s FROM action_pipeline %s ORDER BY n",
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
		"SELECT pipeline, verb FROM pipeline_step ORDER BY pipeline, position")
	if err != nil {
		return fmt.Errorf("reading pipeline steps: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name, verb string
		if err := rows.Scan(&name, &verb); err != nil {
			return fmt.Errorf("reading pipeline steps: %w", err)
		}
		if p, ok := byName[name]; ok {
			p.Steps = append(p.Steps, verb)
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
	clone.Steps = append([]string(nil), p.Steps...)
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
func (t *Tx) CheckPipelineSteps(ctx context.Context, steps []string) error {
	for _, step := range steps {
		v, err := t.LoadVerb(ctx, step)
		if err == sql.ErrNoRows {
			return &PipelineStepError{Verb: step, Reason: "there is no such verb"}
		}
		if err != nil {
			return err
		}
		switch {
		case !v.Active:
			return &PipelineStepError{Verb: step, Reason: "it is retired"}
		case v.Closes != ClosesPredicate:
			return &PipelineStepError{Verb: step,
				Reason: "it closes when a person says so, and a pipeline is what follows a person's work"}
		case v.RequiresRef:
			return &PipelineStepError{Verb: step,
				Reason: "it waits for a ref, and instantiating a step cannot say which one"}
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
func (t *Tx) SetSteps(ctx context.Context, p *Pipeline, steps []string) error {
	if err := t.CheckPipelineSteps(ctx, steps); err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx,
		"DELETE FROM pipeline_step WHERE pipeline = ?", p.Name); err != nil {
		return fmt.Errorf("clearing the steps of %s: %w", p.Name, err)
	}
	for i, step := range steps {
		if _, err := t.tx.ExecContext(ctx,
			"INSERT INTO pipeline_step (pipeline, position, verb) VALUES (?, ?, ?)",
			p.Name, i+1, step); err != nil {
			return fmt.Errorf("writing step %d of %s: %w", i+1, p.Name, err)
		}
	}

	// Steps are rows rather than a column, so the diff that Update produces
	// cannot see them. Logged here instead, as one change to the chain rather
	// than one per row: what a reader wants is what it runs now.
	before, after := strings.Join(p.Steps, " → "), strings.Join(steps, " → ")
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
