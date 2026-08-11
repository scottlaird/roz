package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestSeededPipelinesHaveTheirSteps(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	pipelines, err := st.ListPipelines(ctx, true)
	if err != nil {
		t.Fatalf("ListPipelines() returned error: %v", err)
	}

	want := map[string][]string{
		PipelineReview: {"undraft", "send_for_review", "wait_review", "merge"},
		PipelineDirect: {"undraft", "merge"},
	}
	if len(pipelines) != len(want) {
		t.Fatalf("ListPipelines() returned %d pipelines, want %d", len(pipelines), len(want))
	}
	for _, p := range pipelines {
		if !equalStrings(p.Steps, want[p.Name]) {
			t.Errorf("%s steps = %v, want %v", p.Name, p.Steps, want[p.Name])
		}
	}
}

// TestPipelinesAreOrderedByNumber matters because the first one is the
// default, so the order is the decision.
func TestPipelinesAreOrderedByNumber(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	pipelines, err := st.ListPipelines(ctx, true)
	if err != nil {
		t.Fatalf("ListPipelines() returned error: %v", err)
	}
	for i, p := range pipelines {
		if i > 0 && p.N <= pipelines[i-1].N {
			t.Fatalf("pipelines came back out of order: %v then %v", pipelines[i-1].N, p.N)
		}
	}

	got, err := st.DefaultPipeline(ctx)
	if err != nil {
		t.Fatalf("DefaultPipeline() returned error: %v", err)
	}
	if got.Name != pipelines[0].Name {
		t.Errorf("DefaultPipeline() = %q, want the first listed, %q", got.Name, pipelines[0].Name)
	}
}

// TestDefaultPipelineSkipsRetiredOnes: retiring the usual pipeline is how the
// default moves, so it has to be read as "lowest active", not "lowest".
func TestDefaultPipelineSkipsRetiredOnes(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	retire(t, st, PipelineReview)

	got, err := st.DefaultPipeline(ctx)
	if err != nil {
		t.Fatalf("DefaultPipeline() returned error: %v", err)
	}
	if got.Name != PipelineDirect {
		t.Errorf("DefaultPipeline() = %q, want %q", got.Name, PipelineDirect)
	}

	listed, err := st.ListPipelines(ctx, true)
	if err != nil {
		t.Fatalf("ListPipelines() returned error: %v", err)
	}
	for _, p := range listed {
		if p.Name == PipelineReview {
			t.Errorf("a retired pipeline came back from ListPipelines(activeOnly)")
		}
	}
}

// TestDefaultPipelineWithNoneActive says so rather than guessing.
func TestDefaultPipelineWithNoneActive(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	retire(t, st, PipelineReview)
	retire(t, st, PipelineDirect)

	if _, err := st.DefaultPipeline(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("DefaultPipeline() error = %v, want sql.ErrNoRows", err)
	}
}

// retire deactivates a pipeline. There is no verb for this yet; retiring one
// is rare enough to be a statement someone writes by hand.
func retire(t *testing.T, st *Store, name string) {
	t.Helper()
	if _, err := st.db.Exec("UPDATE action_pipeline SET active = 0 WHERE name = ?", name); err != nil {
		t.Fatalf("retiring %s: %v", name, err)
	}
}

func TestLoadUnknownPipeline(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.LoadPipeline(ctx, "nonesuch"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("LoadPipeline() error = %v, want sql.ErrNoRows", err)
	}
}

// TestPipelineStepNeedsAKnownVerb is the same foreign key that keeps actions
// honest: a pipeline can order verbs, and cannot invent one.
func TestPipelineStepNeedsAKnownVerb(t *testing.T) {
	st := newStore(t)

	_, err := st.db.Exec(
		"INSERT INTO pipeline_step (pipeline, position, verb) VALUES (?, ?, ?)",
		PipelineDirect, 99, "teleport")
	if err == nil {
		t.Error("a step naming an unknown verb was inserted, want a foreign key error")
	}
}

// TestRepoNeedsAKnownPipeline is the other end of the same rule.
func TestRepoNeedsAKnownPipeline(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	r := trackRepo(t, st, "scottlaird/roz")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := r.Clone()
	after.Pipeline = sql.NullString{String: "nonesuch", Valid: true}
	if _, err := tx.Update(ctx, r, after); err == nil {
		t.Error("a repository named a pipeline that does not exist, want a foreign key error")
	}
}

// TestPipelineJSONCarriesItsSteps: the steps are a child table, so the
// record's own columns are not enough to describe it.
func TestPipelineJSONCarriesItsSteps(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	p, err := tx.LoadPipeline(ctx, PipelineDirect)
	if err != nil {
		t.Fatalf("LoadPipeline() returned error: %v", err)
	}

	encoded, err := MarshalRecord(p)
	if err != nil {
		t.Fatalf("MarshalRecord() returned error: %v", err)
	}
	got := string(encoded)
	for _, want := range []string{`"steps":["undraft","merge"]`, `"name":"direct"`} {
		if !strings.Contains(got, want) {
			t.Errorf("MarshalRecord() = %s, want it to contain %s", got, want)
		}
	}
}
