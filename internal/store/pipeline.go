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
