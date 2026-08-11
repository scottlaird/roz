package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPipelineList(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "pipeline", "list", "--db", db)
	if err != nil {
		t.Fatalf("pipeline list returned error: %v", err)
	}
	for _, want := range []string{"review", "direct", "undraft", "send_for_review", "merge"} {
		if !strings.Contains(out, want) {
			t.Errorf("pipeline list does not mention %q:\n%s", want, out)
		}
	}
}

// TestPipelineListJSONCarriesSteps: the steps are the whole content of a
// pipeline, so a JSON consumer that cannot see them has nothing.
func TestPipelineListJSONCarriesSteps(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "pipeline", "list", "--db", db, "-o", "json")
	if err != nil {
		t.Fatalf("pipeline list -o json returned error: %v", err)
	}

	var listed []map[string]any
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if len(listed) == 0 {
		t.Fatal("pipeline list -o json returned nothing")
	}

	first := listed[0]
	if first["name"] != "review" {
		t.Errorf("first pipeline = %#v, want the lowest-numbered one", first["name"])
	}
	steps, ok := first["steps"].([]any)
	if !ok || len(steps) != 4 {
		t.Fatalf("steps = %#v, want the four of them", first["steps"])
	}
	if steps[0] != "undraft" || steps[3] != "merge" {
		t.Errorf("steps = %v, want them in order", steps)
	}
}

// TestTrackTakesTheDefaultPipeline: the default is resolved once, when the
// repository is tracked, rather than read through a NULL later.
func TestTrackTakesTheDefaultPipeline(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "scottlaird/roz")

	if got := repoJSON(t, db, "scottlaird/roz")["pipeline"]; got != "review" {
		t.Errorf("pipeline = %#v, want the lowest-numbered active one", got)
	}
}

// TestTrackWithAnEmptyPipelineStaysUnstated distinguishes saying nothing from
// saying "none of them".
func TestTrackWithAnEmptyPipelineStaysUnstated(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "repo", "track", "--db", db, "scottlaird/roz", "--pipeline", ""); err != nil {
		t.Fatalf("repo track returned error: %v", err)
	}
	if got := repoJSON(t, db, "scottlaird/roz")["pipeline"]; got != nil {
		t.Errorf("pipeline = %#v, want null", got)
	}
}

func TestRepoSetRejectsAnUnknownPipeline(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "scottlaird/roz")

	_, err := runCLI(t, "repo", "set", "--db", db, "scottlaird/roz", "--pipeline", "nonesuch")
	if err == nil {
		t.Fatal("repo set accepted an unknown pipeline, want an error")
	}
	if !strings.Contains(err.Error(), "is not a pipeline") {
		t.Errorf("error = %v, want it to name the pipeline", err)
	}
}
