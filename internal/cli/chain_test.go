package cli

import (
	"strings"
	"testing"
)

// chainable is scottlaird/roz#96 as it is typed: a tracked pull request whose
// repository has a pipeline, and one action added by hand carrying a verb from
// it. Every command here succeeded; nothing about the state looks wrong.
func chainable(t *testing.T, verb string) (db, key string) {
	t.Helper()

	db = initDB(t)
	trackRepo(t, db, "owner/repo")
	key = "owner/repo#812"
	if _, err := runCLI(t, "pr", "track", "--db", db, key); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}
	if _, err := runCLI(t, "action", "add", "--db", db,
		"--title", "do the thing", "--verb", verb, "--pr", key); err != nil {
		t.Fatalf("action add returned error: %v", err)
	}
	return db, key
}

// TestShowNamesThePipelineThatApplies is requirement 2 of #96. The pipeline
// column holds the override, so it reads "-" for a pull request carried by its
// repository's chain and "-" for one carried by nothing at all — and it is
// that ambiguity that makes a hand-made head look safe.
func TestShowNamesThePipelineThatApplies(t *testing.T) {
	db, key := chainable(t, "undraft")

	out, err := runCLI(t, "pr", "show", "--db", db, key)
	if err != nil {
		t.Fatalf("pr show returned error: %v", err)
	}
	chain := detailLine(t, out, "chain")
	for _, want := range []string{"review", "owner/repo", "missing"} {
		if !strings.Contains(chain, want) {
			t.Errorf("chain reads %q, want it to mention %q", chain, want)
		}
	}
	// The column itself still says what it always said: nothing is overridden.
	if got := detailLine(t, out, "pipeline"); got != "-" {
		t.Errorf("pipeline = %q, want %q: it is the override, and nothing overrode it", got, "-")
	}
}

// TestShowAndJSONAgreeOnTheChain. A detail view that says something -o json
// does not is how a reader and an agent end up with different pictures of the
// same row, which is the argument showRecord is one function.
func TestShowAndJSONAgreeOnTheChain(t *testing.T) {
	db, key := chainable(t, "undraft")

	out, err := runCLI(t, "pr", "show", "--db", db, key)
	if err != nil {
		t.Fatalf("pr show returned error: %v", err)
	}
	object := showPRJSON(t, db, key)
	if got, want := object["chain"], detailLine(t, out, "chain"); got != want {
		t.Errorf("chain = %#v in JSON and %q in the table", got, want)
	}
}

// TestChainCreatesWhatIsMissingOnce covers the command and requirement 3 in
// one: the repair works, and running it again is not a second chain.
func TestChainCreatesWhatIsMissingOnce(t *testing.T) {
	db, key := chainable(t, "undraft")

	if _, err := runCLI(t, "action", "close", "--db", db, "NA1"); err != nil {
		t.Fatalf("action close returned error: %v", err)
	}
	// The failure the issue describes: nothing left to do, and a pull request
	// still needing a review watched and a merge clicked.
	queue, err := runCLI(t, "action", "list", "--db", db, "--unblocked")
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if !strings.Contains(queue, "no actions") {
		t.Fatalf("the queue is not empty, so this test is not reproducing #96:\n%s", queue)
	}

	out, err := runCLI(t, "pr", "chain", "--db", db, key)
	if err != nil {
		t.Fatalf("pr chain returned error: %v", err)
	}
	for _, want := range []string{"send_for_review", "wait_review", "merge", "blocked by"} {
		if !strings.Contains(out, want) {
			t.Errorf("pr chain does not report %q:\n%s", want, out)
		}
	}

	again, err := runCLI(t, "pr", "chain", "--db", db, key)
	if err != nil {
		t.Fatalf("second pr chain returned error: %v", err)
	}
	if !strings.Contains(again, "nothing missing") {
		t.Errorf("running it twice reported:\n%s", again)
	}
	if strings.Contains(again, "created") {
		t.Errorf("running it twice created something:\n%s", again)
	}
}

// TestChainRefusesAPullRequestNoPipelineReaches. Not everything is chained,
// and reporting success over nothing would leave somebody believing a queue
// was going to fill up.
func TestChainRefusesAPullRequestNoPipelineReaches(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "owner/repo", "--pipeline", "")
	if _, err := runCLI(t, "pr", "track", "--db", db, "owner/repo#1"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}

	out, err := runCLI(t, "pr", "show", "--db", db, "owner/repo#1")
	if err != nil {
		t.Fatalf("pr show returned error: %v", err)
	}
	if got := detailLine(t, out, "chain"); got != "none" {
		t.Errorf("chain = %q, want %q", got, "none")
	}

	_, err = runCLI(t, "pr", "chain", "--db", db, "owner/repo#1")
	if err == nil {
		t.Fatal("pr chain succeeded with no pipeline, want an error")
	}
	if !strings.Contains(err.Error(), "--pipeline") {
		t.Errorf("error = %v, want it to name the flag that would fix it", err)
	}
}

// detailLine reads one key out of a `show` table.
func detailLine(t *testing.T, out, key string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		name, value, found := strings.Cut(line, " ")
		if found && name == key {
			return strings.TrimSpace(value)
		}
	}
	t.Fatalf("no %q line in:\n%s", key, out)
	return ""
}
