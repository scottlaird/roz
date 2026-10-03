package cli

import (
	"strings"
	"testing"
)

// TestAnInactiveProjectTakesItsActionsOutOfTheQueue: with inactive_priority
// set, an action on a project at that priority or higher is not an answer to
// "what do I do now", and the queue leaves it out. Raising the project's
// priority brings it straight back, with nothing to release.
func TestAnInactiveProjectTakesItsActionsOutOfTheQueue(t *testing.T) {
	db := initDB(t)
	someday := addProject(t, db, "Maybe someday", "--priority", "5")
	active := addProject(t, db, "This quarter", "--priority", "2")
	unset := addProject(t, db, "No priority yet")
	addAction(t, db, "--title", "Someday work", "--verb", "write", "--project", someday)
	addAction(t, db, "--title", "Active work", "--verb", "write", "--project", active)
	addAction(t, db, "--title", "Unprioritised work", "--verb", "write", "--project", unset)
	addAction(t, db, "--title", "Projectless work", "--verb", "write")

	queue := func() string {
		t.Helper()
		out, err := runCLI(t, "action", "list", "--db", db, "--unblocked")
		if err != nil {
			t.Fatalf("action list --unblocked returned error: %v", err)
		}
		return out
	}

	// No cutoff by default: a queue that has never said what its priorities
	// mean keeps showing everything.
	if got := queue(); !strings.Contains(got, "Someday work") {
		t.Fatalf("with no cutoff set, the queue already leaves out a P5 action:\n%s", got)
	}

	setInactivePriority(t, db, "5")
	got := queue()
	if strings.Contains(got, "Someday work") {
		t.Errorf("a P5 action is still in the queue with inactive_priority 5:\n%s", got)
	}
	for _, want := range []string{"Active work", "Unprioritised work", "Projectless work"} {
		if !strings.Contains(got, want) {
			t.Errorf("the cutoff removed %q, which it should not touch:\n%s", want, got)
		}
	}

	// Still listed without --unblocked: inactive is not closed.
	all, err := runCLI(t, "action", "list", "--db", db)
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if !strings.Contains(all, "Someday work") {
		t.Errorf("the action vanished from the full listing:\n%s", all)
	}

	if _, err := runCLI(t, "project", "set", "--db", db, someday, "--priority", "3"); err != nil {
		t.Fatalf("project set returned error: %v", err)
	}
	if got := queue(); !strings.Contains(got, "Someday work") {
		t.Errorf("reprioritising the project did not bring its action back:\n%s", got)
	}
}

// TestAPinnedActionSurvivesAnInactiveProject is the same escape a blocked
// project has: pinning one says "I mean this one".
func TestAPinnedActionSurvivesAnInactiveProject(t *testing.T) {
	db := initDB(t)
	someday := addProject(t, db, "Maybe someday", "--priority", "6")
	buried := addAction(t, db, "--title", "Someday work", "--verb", "write", "--project", someday)
	pinned := addAction(t, db, "--title", "Ask the one question", "--verb", "decide", "--project", someday)
	setInactivePriority(t, db, "5")

	if _, err := runCLI(t, "action", "set", "--db", db, pinned, "--rank-pin", "1"); err != nil {
		t.Fatalf("action set --rank-pin returned error: %v", err)
	}
	out, err := runCLI(t, "action", "list", "--db", db, "--unblocked")
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if !strings.Contains(out, pinned) {
		t.Errorf("the pinned action was left out with the rest:\n%s", out)
	}
	if strings.Contains(out, buried) {
		t.Errorf("pinning one action released the others:\n%s", out)
	}
}

// TestWaitingIsUnaffectedByAnInactiveProject: a wait on GitHub is still true
// whatever the project's priority, as it is for a blocked project.
func TestWaitingIsUnaffectedByAnInactiveProject(t *testing.T) {
	db := initDB(t)
	someday := addProject(t, db, "Maybe someday", "--priority", "5")
	trackRepo(t, db, "acme/api")
	if _, err := runCLI(t, "pr", "track", "--db", db, "acme/api#1"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}
	addAction(t, db, "--title", "Wait for review", "--verb", "wait_review",
		"--project", someday, "--pr", "acme/api#1")
	setInactivePriority(t, db, "5")

	out, err := runCLI(t, "action", "list", "--db", db, "--waiting")
	if err != nil {
		t.Fatalf("action list --waiting returned error: %v", err)
	}
	if !strings.Contains(out, "Wait for review") {
		t.Errorf("--waiting lost a wait on an inactive project:\n%s", out)
	}
}

// TestActionShowSaysItsProjectIsInactive: derived, so nothing on the action
// explains it, and `show` would otherwise say ready about something the
// queue will not offer.
func TestActionShowSaysItsProjectIsInactive(t *testing.T) {
	db := initDB(t)
	someday := addProject(t, db, "Maybe someday", "--priority", "5")
	id := addAction(t, db, "--title", "Someday work", "--verb", "write", "--project", someday)
	setInactivePriority(t, db, "5")

	out, err := runCLI(t, "action", "show", "--db", db, id)
	if err != nil {
		t.Fatalf("action show returned error: %v", err)
	}
	if !strings.Contains(out, "inactive") || !strings.Contains(out, someday+" is P5") {
		t.Errorf("action show does not say the project is inactive:\n%s", out)
	}
}

func TestInactivePriorityMustBeAPriority(t *testing.T) {
	db := initDB(t)
	if _, err := runCLI(t, "config", "set", "--db", db, "--inactive-priority", "10"); err == nil {
		t.Error("config set accepted an inactive priority of 10")
	}
	if _, err := runCLI(t, "config", "set", "--db", db, "--inactive-priority", "-1"); err == nil {
		t.Error("config set accepted an inactive priority of -1")
	}
	setInactivePriority(t, db, "0")
}

func setInactivePriority(t *testing.T, db, priority string) {
	t.Helper()
	if _, err := runCLI(t, "config", "set", "--db", db, "--inactive-priority", priority); err != nil {
		t.Fatalf("config set --inactive-priority returned error: %v", err)
	}
}
