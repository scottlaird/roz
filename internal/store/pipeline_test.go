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
		if !equalStrings(stepVerbs(p.Steps), want[p.Name]) {
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

// stepVerbs is the verbs of a chain, for tests that care about the order
// rather than about what any step waits for.
func stepVerbs(steps []PipelineStep) []string {
	verbs := make([]string, len(steps))
	for i, step := range steps {
		verbs[i] = step.Verb
	}
	return verbs
}

// TestAReleaseGateIsAStep is what #127 asked for: a wait_ref step is writable
// again, because it can now say what it waits for.
func TestAReleaseGateIsAStep(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	ok := [][]PipelineStep{
		{{Verb: "undraft"}, {Verb: "wait_ref", Spec: ">=minor+2"}, {Verb: "merge"}},
		{{Verb: "wait_ref", Spec: "api/>=major+1"}},
	}
	for _, steps := range ok {
		if err := tx.CheckPipelineSteps(ctx, steps); err != nil {
			t.Errorf("CheckPipelineSteps(%v) returned error: %v", steps, err)
		}
	}

	bad := []struct {
		steps  []PipelineStep
		reason string
	}{
		// The wedge #127 opened with: a gate with nothing to wait for is
		// created, never closes, and blocks everything behind it.
		{[]PipelineStep{{Verb: "wait_ref"}}, "with no spec"},
		// A fixed version is the same gate for every pull request forever.
		{[]PipelineStep{{Verb: "wait_ref", Spec: ">=3.6"}}, "naming a version"},
		{[]PipelineStep{{Verb: "wait_ref", Spec: "api/>=3.6"}}, "naming a version in a series"},
		// A verb with nothing to say about a spec should not be given one.
		{[]PipelineStep{{Verb: "merge", Spec: ">=minor+2"}}, "on a verb that takes none"},
		{[]PipelineStep{{Verb: "wait_ref", Spec: "minor+2"}}, "without the operator"},
	}
	for _, tt := range bad {
		if err := tx.CheckPipelineSteps(ctx, tt.steps); err == nil {
			t.Errorf("CheckPipelineSteps accepted a step %s", tt.reason)
		}
	}
}

func TestParseStep(t *testing.T) {
	// canonical is how the step renders back, which is not always how it was
	// typed: spacing inside the brackets is normalised away.
	tests := []struct {
		text, verb, spec, canonical string
	}{
		{"merge", "merge", "", "merge"},
		{"wait_ref(>=minor+2)", "wait_ref", ">=minor+2", "wait_ref(>=minor+2)"},
		{"wait_ref(api/>=minor+2)", "wait_ref", "api/>=minor+2", "wait_ref(api/>=minor+2)"},
		{" merge ", "merge", "", "merge"},
		{"wait_ref( >=minor+2 )", "wait_ref", ">=minor+2", "wait_ref(>=minor+2)"},
	}
	for _, tt := range tests {
		got, err := ParseStep(tt.text)
		if err != nil {
			t.Errorf("ParseStep(%q) returned error: %v", tt.text, err)
			continue
		}
		if got.Verb != tt.verb || got.Spec != tt.spec {
			t.Errorf("ParseStep(%q) = %+v, want %s/%s", tt.text, got, tt.verb, tt.spec)
		}
		if round := got.String(); round != tt.canonical {
			t.Errorf("String() = %q, want %q", round, tt.canonical)
		}
	}
	for _, bad := range []string{
		"(>=minor+2)",        // no verb
		"wait_ref(",          // opened and never closed
		"wait_ref(>=minor+2", //
		"wait_ref)",          // closed and never opened
		"wait_ref()",         // brackets saying nothing
		"",
	} {
		if _, err := ParseStep(bad); err == nil {
			t.Errorf("ParseStep(%q) was accepted", bad)
		}
	}
}

// TestSplitStepsRespectsBrackets is what the brackets buy beyond legibility: a
// spec containing a comma is splittable, where a separator form could never
// tell the two kinds of comma apart.
func TestSplitStepsRespectsBrackets(t *testing.T) {
	tests := []struct {
		text string
		want []string
	}{
		{"undraft,merge", []string{"undraft", "merge"}},
		{"undraft,wait_ref(>=minor+2),merge", []string{"undraft", "wait_ref(>=minor+2)", "merge"}},
		// Nothing writes one of these today; the brackets are what make it
		// possible to later.
		{"wait_ref(>=1.2, <2.0),merge", []string{"wait_ref(>=1.2, <2.0)", "merge"}},
		{"merge", []string{"merge"}},
	}
	for _, tt := range tests {
		got := SplitSteps(tt.text)
		if len(got) != len(tt.want) {
			t.Errorf("SplitSteps(%q) = %q, want %q", tt.text, got, tt.want)
			continue
		}
		for i := range got {
			if strings.TrimSpace(got[i]) != tt.want[i] {
				t.Errorf("SplitSteps(%q) = %q, want %q", tt.text, got, tt.want)
				break
			}
		}
	}
}
