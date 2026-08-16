package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectBlockAndClose(t *testing.T) {
	db := initDB(t)
	blocker := addProject(t, db, "thread the flag through")
	blocked := addProject(t, db, "serve MCP over HTTP")

	out, err := runCLI(t, "project", "block", "--db", db, "--from", blocked, "--to", blocker)
	if err != nil {
		t.Fatalf("project block returned error: %v", err)
	}
	if !strings.Contains(out, "blocked") || !strings.Contains(out, blocker) {
		t.Errorf("block did not report what happened:\n%s", out)
	}

	// The status moved with the edge, so `blocked` finally means something.
	listed, err := runCLI(t, "project", "list", "--db", db)
	if err != nil {
		t.Fatalf("project list returned error: %v", err)
	}
	if !strings.Contains(listed, "blocked") {
		t.Errorf("the project is not shown as blocked:\n%s", listed)
	}

	closed, err := runCLI(t, "project", "close", "--db", db, blocker, "--status", "done")
	if err != nil {
		t.Fatalf("project close returned error: %v", err)
	}
	if !strings.Contains(closed, blocked) || !strings.Contains(closed, "active") {
		t.Errorf("closing the blocker did not report freeing %s:\n%s", blocked, closed)
	}
}

func TestProjectUnblock(t *testing.T) {
	db := initDB(t)
	blocker := addProject(t, db, "thread the flag through")
	blocked := addProject(t, db, "serve MCP over HTTP")

	if _, err := runCLI(t, "project", "block", "--db", db, "--from", blocked, "--to", blocker); err != nil {
		t.Fatalf("project block returned error: %v", err)
	}
	out, err := runCLI(t, "project", "unblock", "--db", db, "--from", blocked, "--to", blocker)
	if err != nil {
		t.Fatalf("project unblock returned error: %v", err)
	}
	if !strings.Contains(out, "active") {
		t.Errorf("unblock did not return it to active:\n%s", out)
	}
}

// TestBlockedProjectIsOnThePage is the reason this was worth doing now: a
// blocked project used to vanish, because the page filtered to active.
func TestBlockedProjectIsOnThePage(t *testing.T) {
	db := initDB(t)
	blocker := addProject(t, db, "thread the flag through")
	blocked := addProject(t, db, "serve MCP over HTTP")
	if _, err := runCLI(t, "project", "block", "--db", db, "--from", blocked, "--to", blocker); err != nil {
		t.Fatalf("project block returned error: %v", err)
	}

	page, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(page, "serve MCP over HTTP") {
		t.Errorf("the blocked project is missing from the page:\n%s", page)
	}
	if !strings.Contains(page, "blocked") {
		t.Errorf("the page does not say it is blocked:\n%s", page)
	}
}

func TestProjectBlockShowsInBothFormats(t *testing.T) {
	db := initDB(t)
	blocker := addProject(t, db, "thread the flag through")
	blocked := addProject(t, db, "serve MCP over HTTP")
	if _, err := runCLI(t, "project", "block", "--db", db, "--from", blocked, "--to", blocker); err != nil {
		t.Fatalf("project block returned error: %v", err)
	}

	table, err := runCLI(t, "project", "show", "--db", db, blocked)
	if err != nil {
		t.Fatalf("project show returned error: %v", err)
	}
	if !strings.Contains(table, "blocked_by") || !strings.Contains(table, blocker) {
		t.Errorf("show does not name the blocker:\n%s", table)
	}

	encoded, err := runCLI(t, "project", "show", "--db", db, blocker, "-o", "json")
	if err != nil {
		t.Fatalf("project show returned error: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(encoded), &object); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	blocking, _ := object["blocking"].([]any)
	if len(blocking) != 1 || blocking[0] != blocked {
		t.Errorf("blocking = %v, want [%s]", object["blocking"], blocked)
	}
}

func TestProjectBlockRejections(t *testing.T) {
	db := initDB(t)
	first := addProject(t, db, "first")
	second := addProject(t, db, "second")
	if _, err := runCLI(t, "project", "block", "--db", db, "--from", second, "--to", first); err != nil {
		t.Fatalf("project block returned error: %v", err)
	}

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"a cycle", []string{"project", "block", "--from", first, "--to", second}, "never unblock"},
		{"itself", []string{"project", "block", "--from", first, "--to", first}, "cannot block itself"},
		{"an unknown project", []string{"project", "block", "--from", "ROZ99", "--to", first}, "ROZ99"},
		{"an edge that is not there", []string{"project", "unblock", "--from", first, "--to", second}, "does not block"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runCLI(t, append(tt.args, "--db", db)...)
			if err == nil {
				t.Fatalf("%v was accepted, want an error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// TestABlockedProjectTakesItsActionsOutOfTheQueue is scottlaird/roz#181. The
// queue answers "what do I do now", and a blocked project's actions are
// exactly the ones that cannot be done now — but the project listing said
// blocked while the action listing said go, about the same fact.
func TestABlockedProjectTakesItsActionsOutOfTheQueue(t *testing.T) {
	db := initDB(t)
	held := addProject(t, db, "Ship the migration")
	other := addProject(t, db, "Upgrade the cluster")
	addAction(t, db, "--title", "Write the endpoint", "--verb", "write", "--project", held)
	addAction(t, db, "--title", "Unrelated work", "--verb", "write", "--project", other)

	queue := func() string {
		t.Helper()
		out, err := runCLI(t, "action", "list", "--db", db, "--unblocked")
		if err != nil {
			t.Fatalf("action list --unblocked returned error: %v", err)
		}
		return out
	}

	if !strings.Contains(queue(), "Write the endpoint") {
		t.Fatal("the action is not in the queue to begin with")
	}

	blockProject(t, db, held, other)

	if got := queue(); strings.Contains(got, "Write the endpoint") {
		t.Errorf("a blocked project's action is still in the queue:\n%s", got)
	}
	if got := queue(); !strings.Contains(got, "Unrelated work") {
		t.Errorf("blocking one project emptied another's queue:\n%s", got)
	}

	// Still listed without --unblocked, and marked. Blocked is where work goes
	// quiet, so hiding it entirely would trade one silence for another.
	all, err := runCLI(t, "action", "list", "--db", db)
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if !strings.Contains(all, "Write the endpoint") {
		t.Errorf("the action vanished from the full listing:\n%s", all)
	}
	if !strings.Contains(all, "HELD") {
		t.Errorf("the full listing does not mark it:\n%s", all)
	}
}

// TestTheQueueComesBackWhenTheLastBlockerCloses: no second command, and
// nobody has to have remembered which actions were affected. Derived at query
// time, so there is nothing to release.
func TestTheQueueComesBackWhenTheLastBlockerCloses(t *testing.T) {
	db := initDB(t)
	held := addProject(t, db, "Ship the migration")
	first := addProject(t, db, "Upgrade the cluster")
	second := addProject(t, db, "Drain the pool")
	addAction(t, db, "--title", "Write the endpoint", "--verb", "write", "--project", held)

	blockProject(t, db, held, first)
	blockProject(t, db, held, second)

	// A project can be blocked by several. The release is "no open blockers
	// remain", not "the blocker I remembered closed".
	if _, err := runCLI(t, "project", "close", "--db", db, first); err != nil {
		t.Fatalf("project close returned error: %v", err)
	}
	out, err := runCLI(t, "action", "list", "--db", db, "--unblocked")
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if strings.Contains(out, "Write the endpoint") {
		t.Errorf("the action returned while a second blocker was still open:\n%s", out)
	}

	if _, err := runCLI(t, "project", "close", "--db", db, second); err != nil {
		t.Fatalf("project close returned error: %v", err)
	}
	out, err = runCLI(t, "action", "list", "--db", db, "--unblocked")
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if !strings.Contains(out, "Write the endpoint") {
		t.Errorf("the action did not come back when the last blocker closed:\n%s", out)
	}
}

// TestAPinnedActionSurvivesItsProjectBeingBlocked is the escape. Not every
// action on a blocked project is blocked by the same thing: the decide that
// would remove the blocker is exactly the work that clears it, and a rule
// applied uniformly buries it.
func TestAPinnedActionSurvivesItsProjectBeingBlocked(t *testing.T) {
	db := initDB(t)
	held := addProject(t, db, "Ship the migration")
	blocker := addProject(t, db, "Upgrade the cluster")
	buried := addAction(t, db, "--title", "Write the endpoint", "--verb", "write", "--project", held)
	clears := addAction(t, db, "--title", "Decide the cutover", "--verb", "decide", "--project", held)

	blockProject(t, db, held, blocker)
	if _, err := runCLI(t, "action", "set", "--db", db, clears, "--rank-pin", "1"); err != nil {
		t.Fatalf("action set --rank-pin returned error: %v", err)
	}

	out, err := runCLI(t, "action", "list", "--db", db, "--unblocked")
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if !strings.Contains(out, clears) {
		t.Errorf("the pinned action was buried with the rest:\n%s", out)
	}
	if strings.Contains(out, buried) {
		t.Errorf("pinning one action released the others:\n%s", out)
	}
}

// TestActionShowSaysWhyItIsOutOfTheQueue: the action carries nothing to
// explain it — deliberately, since this is derived — so `show` would
// otherwise say ready about something the queue will not offer.
func TestActionShowSaysWhyItIsOutOfTheQueue(t *testing.T) {
	db := initDB(t)
	held := addProject(t, db, "Ship the migration")
	blocker := addProject(t, db, "Upgrade the cluster")
	id := addAction(t, db, "--title", "Write the endpoint", "--verb", "write", "--project", held)

	blockProject(t, db, held, blocker)

	out, err := runCLI(t, "action", "show", "--db", db, id)
	if err != nil {
		t.Fatalf("action show returned error: %v", err)
	}
	if !strings.Contains(out, "held_by") {
		t.Errorf("action show does not say it is held:\n%s", out)
	}
	// The blocking project by name, rather than only a state.
	if !strings.Contains(out, blocker) {
		t.Errorf("action show does not name what is holding it:\n%s", out)
	}
}

// TestWaitingIsUnaffectedByABlockedProject: --waiting answers "what am I
// waiting on", and a wait on GitHub is still true whatever the project's
// status says. Hiding it there would lose it from both views.
func TestWaitingIsUnaffectedByABlockedProject(t *testing.T) {
	db := initDB(t)
	held := addProject(t, db, "Ship the migration")
	blocker := addProject(t, db, "Upgrade the cluster")
	trackRepo(t, db, "acme/api")
	if _, err := runCLI(t, "pr", "track", "--db", db, "acme/api#1"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}
	addAction(t, db, "--title", "Wait for review", "--verb", "wait_review",
		"--project", held, "--pr", "acme/api#1")

	blockProject(t, db, held, blocker)

	out, err := runCLI(t, "action", "list", "--db", db, "--waiting")
	if err != nil {
		t.Fatalf("action list --waiting returned error: %v", err)
	}
	if !strings.Contains(out, "Wait for review") {
		t.Errorf("--waiting lost a wait on a blocked project:\n%s", out)
	}
}

// blockProject records that one project is waiting on another.
func blockProject(t *testing.T, db, blocked, blocker string) {
	t.Helper()
	if _, err := runCLI(t, "project", "block", "--db", db,
		"--from", blocked, "--to", blocker); err != nil {
		t.Fatalf("project block returned error: %v", err)
	}
}
