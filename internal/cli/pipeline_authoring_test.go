package cli

import (
	"strings"
	"testing"
)

// TestAPipelineWithNoSteps is the case that prompted the command: a repository
// whose pull requests you review rather than author wants a chain that
// instantiates nothing, and the only way to have one was an INSERT by hand.
func TestAPipelineWithNoSteps(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "pipeline", "add", "reviewing", "--db", db,
		"--label", "somebody else's", "--steps", "")
	if err != nil {
		t.Fatalf("pipeline add returned error: %v", err)
	}
	if !strings.Contains(out, "nothing follows") {
		t.Errorf("add printed %q, want it to say the chain is empty", out)
	}

	// Distinguishable from a repository with no pipeline: this one exists and
	// says so, which is the whole point of allowing it.
	shown, err := runCLI(t, "pipeline", "show", "reviewing", "--db", db)
	if err != nil {
		t.Fatalf("pipeline show returned error: %v", err)
	}
	if !strings.Contains(shown, "no steps") {
		t.Errorf("show printed %q", shown)
	}
}

// TestAStepMustCloseOnItsOwn: a pipeline is what follows a person's work, so a
// step that waits on a person would stop the chain until somebody noticed.
func TestAStepMustCloseOnItsOwn(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "pipeline", "add", "bad", "--db", db, "--steps", "undraft,review")
	if err == nil {
		t.Fatal("pipeline add accepted a human-closed step")
	}
	if !strings.Contains(err.Error(), "review") {
		t.Errorf("error does not name the step: %v", err)
	}
}

// TestAStepCannotNeedSomethingInstantiationLacks: a wait_ref step would be
// created with nothing to wait for and would block everything behind it for
// ever, which is scottlaird/roz#127.
func TestAStepCannotNeedSomethingInstantiationLacks(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "pipeline", "add", "bad", "--db", db, "--steps", "undraft,wait_ref")
	if err == nil {
		t.Fatal("pipeline add accepted a step that can never close")
	}
	if !strings.Contains(err.Error(), "wait_ref") {
		t.Errorf("error does not name the step: %v", err)
	}
}

func TestAnUnknownStepIsRefused(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "pipeline", "add", "bad", "--db", db, "--steps", "undraft,frobnicate")
	if err == nil {
		t.Fatal("pipeline add accepted a verb that does not exist")
	}
	if !strings.Contains(err.Error(), "no such verb") {
		t.Errorf("error does not say the verb is unknown: %v", err)
	}
}

// TestOrderDecidesTheDefault: n is load-bearing, and the command is what makes
// changing it deliberate rather than a number somebody typed.
func TestOrderDecidesTheDefault(t *testing.T) {
	db := initDB(t)

	// Last by default, so adding one never moves the default from under you.
	if _, err := runCLI(t, "pipeline", "add", "late", "--db", db, "--steps", "merge"); err != nil {
		t.Fatalf("pipeline add returned error: %v", err)
	}
	trackRepo(t, db, "acme/first")
	if got := repoPipeline(t, db, "acme/first"); got == "late" {
		t.Errorf("a pipeline added last became the default")
	}

	out, err := runCLI(t, "pipeline", "add", "house", "--db", db,
		"--steps", "merge", "--order", "first")
	if err != nil {
		t.Fatalf("pipeline add returned error: %v", err)
	}
	if !strings.Contains(out, "newly tracked repositories") {
		t.Errorf("add printed %q, want it to say the default moved", out)
	}
	trackRepo(t, db, "acme/second")
	if got := repoPipeline(t, db, "acme/second"); got != "house" {
		t.Errorf("a newly tracked repository took %q, want house", got)
	}
}

// TestRetiringLeavesExistingRepositoriesWorking: retired rather than deleted,
// the way a verb is. What changes is only what a *new* repository takes.
func TestRetiringLeavesExistingRepositoriesWorking(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "pipeline", "add", "fast", "--db", db, "--steps", "merge"); err != nil {
		t.Fatalf("pipeline add returned error: %v", err)
	}
	trackRepo(t, db, "acme/api", "--pipeline", "fast")

	out, err := runCLI(t, "pipeline", "retire", "fast", "--db", db)
	if err != nil {
		t.Fatalf("pipeline retire returned error: %v", err)
	}
	if !strings.Contains(out, "acme/api") {
		t.Errorf("retire printed %q, want it to name what still uses it", out)
	}

	// Still resolvable, so closing a write action against that repository
	// still instantiates its chain rather than failing at the worst moment.
	if got := repoPipeline(t, db, "acme/api"); got != "fast" {
		t.Errorf("the repository's pipeline is %q, want it left alone", got)
	}
	shown, err := runCLI(t, "pipeline", "show", "fast", "--db", db)
	if err != nil {
		t.Fatalf("pipeline show returned error: %v", err)
	}
	if !strings.Contains(shown, "retired") {
		t.Errorf("show printed %q", shown)
	}
}

// TestSettingStepsReplacesThem, and says what the chain became.
func TestSettingStepsReplacesThem(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "pipeline", "add", "fast", "--db", db, "--steps", "undraft,merge"); err != nil {
		t.Fatalf("pipeline add returned error: %v", err)
	}
	out, err := runCLI(t, "pipeline", "set", "fast", "--db", db, "--steps", "undraft,rebase,merge")
	if err != nil {
		t.Fatalf("pipeline set returned error: %v", err)
	}
	if !strings.Contains(out, "undraft → rebase → merge") {
		t.Errorf("set printed %q", out)
	}

	// And emptying it is allowed, since no steps is a legal chain.
	if _, err := runCLI(t, "pipeline", "set", "fast", "--db", db, "--steps", ""); err != nil {
		t.Fatalf("pipeline set returned error: %v", err)
	}
	shown, _ := runCLI(t, "pipeline", "show", "fast", "--db", db)
	if !strings.Contains(shown, "no steps") {
		t.Errorf("show printed %q after the steps were cleared", shown)
	}
}

// TestAuthoringIsInTheLog: the asymmetry the issue names is that choosing a
// pipeline is a first-class operation and defining one was not, so the
// defining half has to earn an actor and an event like everything else.
func TestAuthoringIsInTheLog(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "pipeline", "add", "fast", "--db", db,
		"--steps", "undraft,merge", "--actor", "agent:test"); err != nil {
		t.Fatalf("pipeline add returned error: %v", err)
	}

	out, err := runCLI(t, "watch", "--once", "-n", "50", "--db", db)
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if !strings.Contains(out, "agent:test") {
		t.Errorf("the log does not record who authored it:\n%s", out)
	}
	if !strings.Contains(out, "undraft → merge") {
		t.Errorf("the log does not record the chain:\n%s", out)
	}
}

// TestAddingAPipelineTwiceIsRefused, pointing at the command that changes one.
func TestAddingAPipelineTwiceIsRefused(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "pipeline", "add", "fast", "--db", db, "--steps", "merge"); err != nil {
		t.Fatalf("pipeline add returned error: %v", err)
	}
	_, err := runCLI(t, "pipeline", "add", "fast", "--db", db, "--steps", "merge")
	if err == nil {
		t.Fatal("pipeline add accepted a name that already exists")
	}
	if !strings.Contains(err.Error(), "pipeline set") {
		t.Errorf("error does not point at the command that changes one: %v", err)
	}
}

// repoPipeline reads back which chain a repository resolved to.
func repoPipeline(t *testing.T, db, repo string) string {
	t.Helper()
	out, err := runCLI(t, "repo", "show", repo, "--db", db)
	if err != nil {
		t.Fatalf("repo show returned error: %v", err)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "pipeline") {
			return strings.TrimSpace(strings.TrimPrefix(line, "pipeline"))
		}
	}
	return ""
}
