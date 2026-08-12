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

// TestVerbSetChangesTheAllowance: tuning a number that is meant to be tuned
// should not mean a migration and a rebuild.
func TestVerbSetChangesTheAllowance(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "verb", "set", "wait_review", "--db", db, "--wait-days", "1")
	if err != nil {
		t.Fatalf("verb set returned error: %v", err)
	}
	if !strings.Contains(out, `wait_days: "3" → "1"`) {
		t.Errorf("verb set said %q, want the diff from the seeded allowance", out)
	}
	if got := verbJSON(t, db, "wait_review")["wait_days"]; got != float64(1) {
		t.Errorf("wait_days = %#v, want 1", got)
	}
}

// TestVerbSetClearsTheAllowance: empty means the verb never times out, which
// is right for anything describing your own work.
func TestVerbSetClearsTheAllowance(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "verb", "set", "wait_review", "--db", db, "--wait-days", ""); err != nil {
		t.Fatalf("verb set returned error: %v", err)
	}
	if got := verbJSON(t, db, "wait_review")["wait_days"]; got != nil {
		t.Errorf("wait_days = %#v, want null", got)
	}
}

// TestVerbSetIsLogged: verb rows are authored, so a change to how long waiting
// is reasonable belongs in the log like any other judgement.
func TestVerbSetIsLogged(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "verb", "set", "wait_review", "--db", db,
		"--wait-days", "1", "--actor", "agent:claude"); err != nil {
		t.Fatalf("verb set returned error: %v", err)
	}

	log, err := runCLI(t, "watch", "--once", "--db", db, "-n", "1")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	for _, want := range []string{"agent:claude", "wait_review", "wait_days"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log entry is missing %q:\n%s", want, log)
		}
	}
}

func TestVerbSetRejections(t *testing.T) {
	db := initDB(t)

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"not a number", []string{"wait_review", "--wait-days", "soon"}, "whole number of days"},
		{"negative", []string{"wait_review", "--wait-days", "-2"}, "whole number of days"},
		{"unknown rank class", []string{"wait_review", "--rank-class", "urgent"}, "not recognised"},
		{"unknown verb", []string{"nonsuch", "--wait-days", "1"}, "nonsuch"},
		{"nothing to set", []string{"wait_review"}, "nothing to set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runCLI(t, append([]string{"verb", "set", "--db", db}, tc.args...)...)
			if err == nil {
				t.Fatalf("verb set %v returned nil, want an error", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestVerbSetLeavesTheDefinitionAlone. closes and the predicate are checked
// against the build when the database opens; editing them from here would turn
// that startup check into a failure at closing time.
func TestVerbSetLeavesTheDefinitionAlone(t *testing.T) {
	db := initDB(t)

	for _, flag := range []string{"--closes", "--predicate", "--predicate-key"} {
		if _, err := runCLI(t, "verb", "set", "wait_review", "--db", db, flag, "whatever"); err == nil {
			t.Errorf("verb set accepted %s, want no such flag", flag)
		}
	}
}

// verbJSON reads one verb out of `verb list -o json`.
func verbJSON(t *testing.T, db, verb string) map[string]any {
	t.Helper()

	out, err := runCLI(t, "verb", "list", "--db", db, "-o", "json")
	if err != nil {
		t.Fatalf("verb list returned error: %v", err)
	}
	var verbs []map[string]any
	if err := json.Unmarshal([]byte(out), &verbs); err != nil {
		t.Fatalf("verb list is not JSON: %v", err)
	}
	for _, v := range verbs {
		if v["verb"] == verb {
			return v
		}
	}
	t.Fatalf("%s is not in the vocabulary", verb)
	return nil
}
