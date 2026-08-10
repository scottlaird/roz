package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVerbList(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "verb", "list", "--db", db)
	if err != nil {
		t.Fatalf("verb list returned error: %v", err)
	}

	// The two halves of the vocabulary, and the column that ties a verb to a
	// function in the code.
	for _, want := range []string{"undraft", "pr_not_draft", "predicate", "decide", "human", "PREDICATE"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
	// A human-closed verb has no predicate.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "decide") && strings.Contains(line, "pr_") {
			t.Errorf("a human-closed verb names a predicate: %s", line)
		}
	}
}

func TestVerbListJSON(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "verb", "list", "--db", db, "-o", "json")
	if err != nil {
		t.Fatalf("verb list -o json returned error: %v", err)
	}
	var verbs []map[string]any
	if err := json.Unmarshal([]byte(out), &verbs); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if len(verbs) < 10 {
		t.Errorf("got %d verbs, want the whole vocabulary", len(verbs))
	}

	byVerb := map[string]map[string]any{}
	for _, v := range verbs {
		byVerb[v["verb"].(string)] = v
	}
	if got := byVerb["merge"]["predicate_key"]; got != "pr_merged" {
		t.Errorf("merge predicate_key = %#v, want pr_merged", got)
	}
	if got := byVerb["write"]["predicate_key"]; got != nil {
		t.Errorf("write predicate_key = %#v, want null", got)
	}
	if got := byVerb["wait_review"]["rank_class"]; got != "wait" {
		t.Errorf("wait_review rank_class = %#v, want wait", got)
	}
}

// TestVerbListHidesRetired checks the default view, since verbs are retired
// rather than deleted and a retired one is noise.
func TestVerbListHidesRetired(t *testing.T) {
	db := initDB(t)

	active, err := runCLI(t, "verb", "list", "--db", db)
	if err != nil {
		t.Fatalf("verb list returned error: %v", err)
	}
	all, err := runCLI(t, "verb", "list", "--db", db, "--all")
	if err != nil {
		t.Fatalf("verb list --all returned error: %v", err)
	}
	// Nothing is retired yet, so both agree; the flag exists for when
	// something is.
	if len(strings.Split(active, "\n")) != len(strings.Split(all, "\n")) {
		t.Errorf("active and --all differ with nothing retired:\n%s\n---\n%s", active, all)
	}
}
