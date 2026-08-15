package cli

import (
	"strings"
	"testing"
)

// TestATieredPipelineInstantiatesItsGroups is the shape #100 asked for: a
// change needing several reviews in order, by different groups, expressed as
// ordinary steps. The chain machinery already sequences them; all that was
// missing was a step being able to say who it waits for.
func TestATieredPipelineInstantiatesItsGroups(t *testing.T) {
	db, key := trackedPR(t)

	if _, err := runCLI(t, "pipeline", "add", "--db", db, "tiered",
		"--steps", "undraft,wait_review_from(@org/storage),wait_review_from(@org/security),merge",
	); err != nil {
		t.Fatalf("pipeline add returned error: %v", err)
	}
	if _, err := runCLI(t, "pr", "set", "--db", db, key, "--pipeline", "tiered"); err != nil {
		t.Fatalf("pr set --pipeline returned error: %v", err)
	}

	id := addAction(t, db, "--title", "write it", "--verb", "write", "--pr", key)
	out, err := runCLI(t, "action", "close", "--db", db, id)
	if err != nil {
		t.Fatalf("action close returned error: %v", err)
	}
	for _, want := range []string{"@org/storage", "@org/security"} {
		if !strings.Contains(out, want) {
			t.Errorf("the chain does not name %s:\n%s", want, out)
		}
	}

	// Each step carries its own group, which is what makes them different
	// waits rather than the same wait twice.
	listed, err := runCLI(t, "action", "list", "--db", db, "--fields", "verb,waiting_for")
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	for _, want := range []string{"@org/storage", "@org/security"} {
		if !strings.Contains(listed, want) {
			t.Errorf("no step waits for %s:\n%s", want, listed)
		}
	}
}

// TestAStepWithoutItsGroupIsRefusedWhereItIsWritten: instantiated, it would be
// a wait that can never close, blocking everything behind it — and the pull
// request that found out would be the wrong place to learn it.
func TestAStepWithoutItsGroupIsRefusedWhereItIsWritten(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "pipeline", "add", "--db", db, "tiered",
		"--steps", "undraft,wait_review_from,merge")
	if err == nil {
		t.Fatal("pipeline add accepted a review step with no group")
	}
	if !strings.Contains(err.Error(), "whose") {
		t.Errorf("error does not say what is missing: %v", err)
	}
}
