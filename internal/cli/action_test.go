package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/scottlaird/roz/internal/github"
)

// addAction creates one action and returns its id.
func addAction(t *testing.T, db string, extra ...string) string {
	t.Helper()
	args := append([]string{"action", "add", "--db", db}, extra...)
	out, err := runCLI(t, args...)
	if err != nil {
		t.Fatalf("action add returned error: %v", err)
	}
	return strings.TrimSpace(out)
}

func TestActionAddAndList(t *testing.T) {
	db := initDB(t)

	id := addAction(t, db, "--title", "Write the endpoint", "--verb", "write")
	if id != "NA1" {
		t.Errorf("action add printed %q, want NA1", id)
	}

	out, err := runCLI(t, "action", "list", "--db", db)
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	for _, want := range []string{"NA1", "ready", "write", "Write the endpoint"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output does not contain %q:\n%s", want, out)
		}
	}
}

func TestActionListEmpty(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "action", "list", "--db", db)
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if !strings.Contains(out, "no actions") {
		t.Errorf("empty list printed %q", out)
	}
}

func TestActionAdvancesAProject(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "Split the nodepool")

	id := addAction(t, db, "--title", "Write it", "--verb", "write",
		"--project", project, "--why", "unblocks the split")

	object := showActionJSON(t, db, id)
	if object["project_id"] != project {
		t.Errorf("project_id = %#v, want %q", object["project_id"], project)
	}
	if object["why"] != "unblocks the split" {
		t.Errorf("why = %#v", object["why"])
	}
}

func showActionJSON(t *testing.T, db, id string) map[string]any {
	t.Helper()
	out, err := runCLI(t, "action", "show", "--db", db, id, "-o", "json")
	if err != nil {
		t.Fatalf("action show returned error: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(out), &object); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	return object
}

// TestActionSnoozeAndWake covers the pair, since the schema couples state and
// snooze_until.
func TestActionSnoozeAndWake(t *testing.T) {
	db := initDB(t)
	id := addAction(t, db, "--title", "Later", "--verb", "investigate")

	if _, err := runCLI(t, "action", "snooze", "--db", db, id,
		"--snooze-until", "2026-08-21", "--snooze-reason", "after oncall"); err != nil {
		t.Fatalf("action snooze returned error: %v", err)
	}
	if got := showActionJSON(t, db, id)["state"]; got != "snoozed" {
		t.Errorf("state = %#v, want snoozed", got)
	}

	if _, err := runCLI(t, "action", "wake", "--db", db, id); err != nil {
		t.Fatalf("action wake returned error: %v", err)
	}
	object := showActionJSON(t, db, id)
	if object["state"] != "ready" || object["snooze_until"] != nil {
		t.Errorf("after wake = %#v, want ready with no date", object)
	}
}

// TestExpiredFindsAForgottenSnooze is the query the sketch calls the highest
// value in the system.
func TestExpiredFindsAForgottenSnooze(t *testing.T) {
	db := initDB(t)
	id := addAction(t, db, "--title", "Revisit Thursday", "--verb", "decide")

	if _, err := runCLI(t, "action", "snooze", "--db", db, id, "--snooze-until", "2000-01-01"); err != nil {
		t.Fatalf("action snooze returned error: %v", err)
	}

	out, err := runCLI(t, "action", "list", "--db", db, "--expired")
	if err != nil {
		t.Fatalf("action list --expired returned error: %v", err)
	}
	if !strings.Contains(out, id) {
		t.Errorf("--expired did not surface the past snooze:\n%s", out)
	}
}

func TestActionSet(t *testing.T) {
	db := initDB(t)
	id := addAction(t, db, "--title", "Write it", "--verb", "write")

	out, err := runCLI(t, "action", "set", "--db", db, id, "--title", "Write it properly", "--verb", "decide")
	if err != nil {
		t.Fatalf("action set returned error: %v", err)
	}
	for _, want := range []string{"title", "verb", "decide"} {
		if !strings.Contains(out, want) {
			t.Errorf("set output does not mention %q:\n%s", want, out)
		}
	}
}

func TestActionRejections(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "unknown verb",
			args:    []string{"action", "add", "--title", "x", "--verb", "teleport"},
			wantErr: "is not a verb",
		},
		{
			name:    "missing project",
			args:    []string{"action", "add", "--title", "x", "--verb", "write", "--project", "ROZ404"},
			wantErr: "no such item: ROZ404",
		},
		{
			name:    "closing through set",
			args:    []string{"action", "set", "NA1", "--status", "done"},
			wantErr: "use `roz action close",
		},
		{
			name:    "a snooze date without the state",
			args:    []string{"action", "set", "NA1", "--snooze-until", "2026-08-21"},
			wantErr: "needs state snoozed",
		},
		{
			name:    "a vague snooze",
			args:    []string{"action", "snooze", "NA1", "--snooze-until", "next week"},
			wantErr: "not a date or timestamp",
		},
		{
			name:    "waking something that is not snoozed",
			args:    []string{"action", "wake", "NA1"},
			wantErr: "not snoozed",
		},
		{
			name:    "set with nothing to set",
			args:    []string{"action", "set", "NA1"},
			wantErr: "nothing to set",
		},
		{
			name:    "raw HTML in prose",
			args:    []string{"action", "add", "--title", "x", "--verb", "write", "--why", "see <b>this</b>"},
			wantErr: "raw HTML is not allowed",
		},
		{
			name:    "sync actor",
			args:    []string{"action", "add", "--title", "x", "--verb", "write", "--actor", "sync:github"},
			wantErr: "not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := initDB(t)
			addAction(t, db, "--title", "an action", "--verb", "write")

			args := append(tt.args, "--db", db)
			_, err := runCLI(t, args...)
			if err == nil {
				t.Fatalf("%v returned nil, want an error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestRejectedActionConsumesNoIdentifier: validation happens before
// allocation, so a typo does not cost a number.
func TestRejectedActionConsumesNoIdentifier(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "action", "add", "--db", db, "--title", "x", "--verb", "teleport"); err == nil {
		t.Fatal("an unknown verb was accepted, want an error")
	}
	if got := addAction(t, db, "--title", "the first real one", "--verb", "write"); got != "NA1" {
		t.Errorf("first successful add produced %q, want NA1", got)
	}
}

// TestActionEventsAreLogged checks the diff-and-emit path reaches actions
// like everything else.
func TestActionEventsAreLogged(t *testing.T) {
	db := initDB(t)
	id := addAction(t, db, "--title", "Write it", "--verb", "write")

	if _, err := runCLI(t, "action", "set", "--db", db, id, "--why", "unblocks the split"); err != nil {
		t.Fatalf("action set returned error: %v", err)
	}

	out, err := runCLI(t, "watch", "--db", db, "--once", "--since", "2000-01-01")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if !strings.Contains(out, "created") || !strings.Contains(out, "why") {
		t.Errorf("the log does not carry the action's history:\n%s", out)
	}
}

// TestActionListUnblocked is the queue: what could be picked up now.
func TestActionListUnblocked(t *testing.T) {
	db := initDB(t)
	ready := addAction(t, db, "--title", "do this one", "--verb", "write")
	blocked := addAction(t, db, "--title", "waits on the first", "--verb", "run")
	hidden := addAction(t, db, "--title", "nothing to do about it", "--verb", "write")

	if _, err := runCLI(t, "action", "add-blocker", "--db", db, "--from", blocked, "--to", ready); err != nil {
		t.Fatalf("action add-blocker returned error: %v", err)
	}
	if _, err := runCLI(t, "action", "hide-behind", "--db", db, "--action", hidden, "--behind", ready); err != nil {
		t.Fatalf("action hide-behind returned error: %v", err)
	}

	out, err := runCLI(t, "action", "list", "--db", db, "--unblocked")
	if err != nil {
		t.Fatalf("action list --unblocked returned error: %v", err)
	}
	if !strings.Contains(out, "do this one") {
		t.Errorf("--unblocked left out the one thing that is ready:\n%s", out)
	}
	for _, unwanted := range []string{"waits on the first", "nothing to do about it"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("--unblocked included %q:\n%s", unwanted, out)
		}
	}

	// Closing the blocker puts the dependent in the queue and takes the
	// hidden one out of hiding.
	if _, err := runCLI(t, "action", "close", "--db", db, ready); err != nil {
		t.Fatalf("action close returned error: %v", err)
	}
	out, err = runCLI(t, "action", "list", "--db", db, "--unblocked")
	if err != nil {
		t.Fatalf("action list --unblocked returned error: %v", err)
	}
	for _, want := range []string{"waits on the first", "nothing to do about it"} {
		if !strings.Contains(out, want) {
			t.Errorf("--unblocked does not include %q after the blocker closed:\n%s", want, out)
		}
	}
}

// TestActionListWaiting is the other half of the queue: what it leaves out
// because somebody else has it.
func TestActionListWaiting(t *testing.T) {
	// A wait verb closes on its pull request, so it needs one to exist at all.
	db, pr := trackedPR(t)
	addAction(t, db, "--title", "write it", "--verb", "write")
	addAction(t, db, "--title", "wait for review", "--verb", "wait_review", "--pr", pr)

	queue, err := runCLI(t, "action", "list", "--db", db, "--unblocked")
	if err != nil {
		t.Fatalf("action list --unblocked returned error: %v", err)
	}
	if !strings.Contains(queue, "write it") || strings.Contains(queue, "wait for review") {
		t.Errorf("--unblocked = \n%s", queue)
	}

	waiting, err := runCLI(t, "action", "list", "--db", db, "--waiting")
	if err != nil {
		t.Fatalf("action list --waiting returned error: %v", err)
	}
	if !strings.Contains(waiting, "wait for review") || strings.Contains(waiting, "write it") {
		t.Errorf("--waiting = \n%s", waiting)
	}
}

// TestActionListStale is the check nothing else performs: an action saying it
// is finished while GitHub says the pull request is open.
func TestActionListStale(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "owner/repo")
	if _, err := runCLI(t, "pr", "track", "--db", db, "owner/repo#1"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}
	withFetcher(t, stubFetcher{result: github.Result{
		PullRequests: []github.PullRequest{{
			Key: "owner/repo#1", Repo: "owner/repo", Number: 1,
			Title: "still open", State: "OPEN", BaseRef: "main",
		}},
	}})
	if _, err := runCLI(t, "sync", "github", "--db", db, "--quiet"); err != nil {
		t.Fatalf("sync returned error: %v", err)
	}

	id := addAction(t, db, "--title", "merge it", "--verb", "merge", "--pr", "owner/repo#1")
	if _, err := runCLI(t, "action", "close", "--db", db, id); err != nil {
		t.Fatalf("action close returned error: %v", err)
	}

	out, err := runCLI(t, "action", "list", "--db", db, "--stale")
	if err != nil {
		t.Fatalf("action list --stale returned error: %v", err)
	}
	if !strings.Contains(out, id) {
		t.Errorf("--stale did not surface the contradiction:\n%s", out)
	}
}

// TestActionAddRefusesAPredicateVerbWithNoSubject: the action would be created
// ready, enter the queue, and never close, because its predicate has nothing
// to ask.
func TestActionAddRefusesAPredicateVerbWithNoSubject(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "action", "add", "--db", db, "--title", "bring it up to date", "--verb", "rebase")
	if err == nil {
		t.Fatal("action add with a predicate verb and no pull request returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "pr_mergeable") {
		t.Errorf("error = %v, want it to name the predicate that would have nothing to ask", err)
	}
	if !strings.Contains(err.Error(), "--pr") {
		t.Errorf("error = %v, want it to name the flag that fixes this", err)
	}
}

// TestActionAddTakesThePullRequestInline keeps the refusal above from forcing
// a two-step add-then-link.
func TestActionAddTakesThePullRequestInline(t *testing.T) {
	db, pr := trackedPR(t)

	id := addAction(t, db, "--title", "bring it up to date", "--verb", "rebase", "--pr", pr)
	if got := showActionJSON(t, db, id)["subject_pr"]; got != pr {
		t.Errorf("subject_pr = %#v, want %s", got, pr)
	}
}

// TestActionAddAllowsAHumanVerbCarryingAPullRequest: `review` has requires_pr
// set and still closes on a person, so the check must not catch it. Keying on
// the column alone would.
func TestActionAddAllowsAHumanVerbCarryingAPullRequest(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "action", "add", "--db", db,
		"--title", "look at someone else's", "--verb", "review"); err != nil {
		t.Errorf("action add --verb review returned error: %v", err)
	}
}

// TestActionSetRefusesMovingOntoAPredicateVerbWithNoSubject: the same gap,
// reached by changing the verb after creation.
func TestActionSetRefusesMovingOntoAPredicateVerbWithNoSubject(t *testing.T) {
	db := initDB(t)
	id := addAction(t, db, "--title", "write it", "--verb", "write")

	_, err := runCLI(t, "action", "set", id, "--db", db, "--verb", "merge")
	if err == nil {
		t.Fatal("action set onto a predicate verb with no subject returned nil, want an error")
	}
	// `action set` has no --pr, so the error must not tell anyone to pass one.
	if strings.Contains(err.Error(), "--pr,") {
		t.Errorf("error = %v, want it to name link-pr rather than a flag this command lacks", err)
	}
	if !strings.Contains(err.Error(), "link-pr") {
		t.Errorf("error = %v, want it to name link-pr", err)
	}
}

func TestActionSetAllowsAPredicateVerbWhenTheSubjectIsThere(t *testing.T) {
	db, pr := trackedPR(t)
	id := addAction(t, db, "--title", "bring it up to date", "--verb", "rebase", "--pr", pr)

	if _, err := runCLI(t, "action", "set", id, "--db", db, "--verb", "merge"); err != nil {
		t.Errorf("action set onto a predicate verb with a subject returned error: %v", err)
	}
}

// mergedPR returns a database holding one pull request already observed as
// merged, with nothing yet pointing at it. Any predicate action linked to it
// afterwards is satisfiable the moment the link exists.
func mergedPR(t *testing.T) (db, key string) {
	t.Helper()
	db, key = trackedPR(t)
	withFetcher(t, stubFetcher{result: github.Result{
		PullRequests: []github.PullRequest{{
			Key: key, Repo: "owner/repo", Number: 1,
			Title: "already merged", State: "MERGED", BaseRef: "main",
		}},
	}})
	if _, err := runCLI(t, "sync", "github", "--db", db, "--quiet"); err != nil {
		t.Fatalf("sync returned error: %v", err)
	}
	return db, key
}

// TestActionAddWithASatisfiedPullRequestSettles: the fact arrived from a
// command rather than a poll, and the action should not wait for the next one.
func TestActionAddWithASatisfiedPullRequestSettles(t *testing.T) {
	db, key := mergedPR(t)

	out, err := runCLI(t, "action", "add", "--db", db,
		"--title", "merge it", "--verb", "merge", "--pr", key)
	if err != nil {
		t.Fatalf("action add returned error: %v", err)
	}
	if !strings.Contains(out, "closed: "+key+" is merge") {
		t.Errorf("action add did not settle:\n%s", out)
	}
	id := strings.Fields(out)[0]
	if got := showActionJSON(t, db, id)["state"]; got != "done" {
		t.Errorf("state = %#v, want done", got)
	}
}

// TestActionSetVerbSettles is the door that is left once a predicate verb
// cannot be created without a subject: change the verb of an action that
// already has one.
func TestActionSetVerbSettles(t *testing.T) {
	db, key := mergedPR(t)
	id := addAction(t, db, "--title", "do it", "--verb", "write")
	if _, err := runCLI(t, "action", "link-pr", "--db", db,
		"--action", id, "--pr", key, "--role", "subject"); err != nil {
		t.Fatalf("action link-pr returned error: %v", err)
	}

	out, err := runCLI(t, "action", "set", id, "--db", db, "--verb", "merge")
	if err != nil {
		t.Fatalf("action set returned error: %v", err)
	}
	if !strings.Contains(out, "closed: "+key+" is merge") {
		t.Errorf("action set --verb did not settle:\n%s", out)
	}
}

// TestLinkPRSettlesNothingOnAHumanVerb: linking a subject to an action that
// closes on a person must not close it. Only a predicate is asked.
func TestLinkPRSettlesNothingOnAHumanVerb(t *testing.T) {
	db, key := mergedPR(t)
	id := addAction(t, db, "--title", "review it", "--verb", "review")

	out, err := runCLI(t, "action", "link-pr", "--db", db,
		"--action", id, "--pr", key, "--role", "subject")
	if err != nil {
		t.Fatalf("action link-pr returned error: %v", err)
	}
	if strings.Contains(out, "closed") {
		t.Errorf("a human verb was closed by linking:\n%s", out)
	}
	if got := showActionJSON(t, db, id)["state"]; got != "ready" {
		t.Errorf("state = %#v, want ready", got)
	}
}

// TestActionAddSettlesNothingWhenThePullRequestIsUnobserved: every predicate
// is false where nothing has been observed, so this has to be a no-op rather
// than a verdict.
func TestActionAddSettlesNothingWhenThePullRequestIsUnobserved(t *testing.T) {
	db, key := trackedPR(t) // tracked, never synced

	out, err := runCLI(t, "action", "add", "--db", db,
		"--title", "merge it", "--verb", "merge", "--pr", key)
	if err != nil {
		t.Fatalf("action add returned error: %v", err)
	}
	if strings.Contains(out, "closed") {
		t.Errorf("an unobserved pull request settled something:\n%s", out)
	}
	if got := showActionJSON(t, db, strings.Fields(out)[0])["state"]; got != "ready" {
		t.Errorf("state = %#v, want ready", got)
	}
}
