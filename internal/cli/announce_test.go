package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func announceable(t *testing.T) (db, key string) {
	t.Helper()
	db = initDB(t)
	trackRepo(t, db, "owner/repo")
	if _, err := runCLI(t, "pr", "track", "--db", db, "owner/repo#1"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}
	return db, "owner/repo#1"
}

func TestAnnounceRecordsTheChannel(t *testing.T) {
	db, key := announceable(t)

	out, err := runCLI(t, "pr", "announce", "--db", db, key, "--channel", "#reviews")
	if err != nil {
		t.Fatalf("pr announce returned error: %v", err)
	}
	for _, want := range []string{"announced_at", "announced_channel", "#reviews"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}

	pr := showPRJSON(t, db, key)
	if pr["announced_channel"] != "#reviews" {
		t.Errorf("announced_channel = %#v, want #reviews", pr["announced_channel"])
	}
	if pr["announced_at"] == nil {
		t.Error("announced_at is null after announcing")
	}
}

// showPRJSON reads a pull request back as a decoded object.
func showPRJSON(t *testing.T, db, key string) map[string]any {
	t.Helper()
	out, err := runCLI(t, "pr", "show", "--db", db, key, "-o", "json")
	if err != nil {
		t.Fatalf("pr show returned error: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(out), &object); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	return object
}

// TestAnnouncingFreezes is why this matters: freezing is what turns the
// amend-versus-new-commit rule into a computed fact.
func TestAnnouncingFreezes(t *testing.T) {
	db, key := announceable(t)

	if got := showPRJSON(t, db, key)["frozen"]; got != false {
		t.Fatalf("frozen = %#v before announcing, want false", got)
	}
	if _, err := runCLI(t, "pr", "announce", "--db", db, key, "--channel", "#reviews"); err != nil {
		t.Fatalf("pr announce returned error: %v", err)
	}
	if got := showPRJSON(t, db, key)["frozen"]; got != true {
		t.Errorf("frozen = %#v after announcing, want true", got)
	}

	// And it shows up in the filter the rule is read through.
	out, err := runCLI(t, "pr", "list", "--db", db, "--frozen")
	if err != nil {
		t.Fatalf("pr list --frozen returned error: %v", err)
	}
	if !strings.Contains(out, key) {
		t.Errorf("pr list --frozen does not include it:\n%s", out)
	}
}

// TestAnnounceIsLoggedAsManual keeps the log honest: it must not claim Slack
// reported something a person typed in.
func TestAnnounceIsLoggedAsManual(t *testing.T) {
	db, key := announceable(t)

	if _, err := runCLI(t, "pr", "announce", "--db", db, key, "--channel", "#reviews"); err != nil {
		t.Fatalf("pr announce returned error: %v", err)
	}

	out, err := runCLI(t, "watch", "--db", db, "--once", "--kind", "changed", "--since", "2000-01-01")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if !strings.Contains(out, "sync:slack-manual") {
		t.Errorf("log does not attribute the change to sync:slack-manual:\n%s", out)
	}
	if strings.Contains(out, " sync:slack ") {
		t.Errorf("log claims Slack reported it:\n%s", out)
	}
}

func TestAnnounceAcceptsAnExplicitTime(t *testing.T) {
	db, key := announceable(t)

	if _, err := runCLI(t, "pr", "announce", "--db", db, key,
		"--channel", "#reviews", "--at", "2026-08-01"); err != nil {
		t.Fatalf("pr announce returned error: %v", err)
	}
	if got := showPRJSON(t, db, key)["announced_at"]; got != "2026-08-01" {
		t.Errorf("announced_at = %#v, want the given date", got)
	}
}

func TestAnnounceIsIdempotentInEffect(t *testing.T) {
	db, key := announceable(t)

	if _, err := runCLI(t, "pr", "announce", "--db", db, key,
		"--channel", "#reviews", "--at", "2026-08-01"); err != nil {
		t.Fatalf("pr announce returned error: %v", err)
	}
	out, err := runCLI(t, "pr", "announce", "--db", db, key,
		"--channel", "#reviews", "--at", "2026-08-01")
	if err != nil {
		t.Fatalf("second announce returned error: %v", err)
	}
	if !strings.Contains(out, "unchanged") {
		t.Errorf("announcing the same thing twice reported %q, want unchanged", out)
	}
}

func TestAnnounceRejections(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "vague time",
			args:    []string{"owner/repo#1", "--channel", "#x", "--at", "yesterday"},
			wantErr: "not a date or timestamp",
		},
		{
			name:    "empty channel",
			args:    []string{"owner/repo#1", "--channel", ""},
			wantErr: "cannot be empty",
		},
		{
			name:    "untracked pull request",
			args:    []string{"owner/repo#404", "--channel", "#x"},
			wantErr: "no such item",
		},
		{
			name:    "not a pull request key",
			args:    []string{"nonsense", "--channel", "#x"},
			wantErr: "not a pull request key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, _ := announceable(t)
			args := append([]string{"pr", "announce", "--db", db}, tt.args...)

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

// TestAnnounceHasNoActorFlag pins the shape of the exception: the actor comes
// from the command, so there is no way to spell a sync actor by hand.
func TestAnnounceHasNoActorFlag(t *testing.T) {
	db, key := announceable(t)

	_, err := runCLI(t, "pr", "announce", "--db", db, key,
		"--channel", "#reviews", "--actor", "sync:slack")
	if err == nil {
		t.Fatal("pr announce accepted --actor, want it rejected")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("error = %v, want an unknown flag", err)
	}
}

// TestAnnouncingSettlesTheStep is SL6: send_for_review closes on the
// announcement, and the announcement has just arrived. Waiting for the next
// poll leaves the queue showing work that is already done for up to a whole
// sync interval.
func TestAnnouncingSettlesTheStep(t *testing.T) {
	db, key := announceable(t)

	// Closing a write action instantiates the repository's review pipeline,
	// whose first steps are undraft and send_for_review.
	work := addAction(t, db, "--title", "write the endpoint", "--verb", "write")
	if _, err := runCLI(t, "action", "close", "--db", db, work, "--pr", key); err != nil {
		t.Fatalf("action close returned error: %v", err)
	}

	step := actionWithVerb(t, db, "send_for_review")
	if step == "" {
		t.Fatal("closing the write action did not instantiate send_for_review")
	}

	out, err := runCLI(t, "pr", "announce", "--db", db, key, "--channel", "#reviews")
	if err != nil {
		t.Fatalf("pr announce returned error: %v", err)
	}

	// It says what it closed. An action closing without anyone asking is the
	// most surprising thing here, and finding out later is worse.
	if !strings.Contains(out, step) || !strings.Contains(out, "closed") {
		t.Errorf("announce did not report closing %s:\n%s", step, out)
	}

	shown, err := runCLI(t, "action", "show", "--db", db, step, "-o", "json")
	if err != nil {
		t.Fatalf("action show returned error: %v", err)
	}
	var action map[string]any
	if err := json.Unmarshal([]byte(shown), &action); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if action["state"] != "done" {
		t.Errorf("%s is %v after the announcement, want done", step, action["state"])
	}
}

// TestAnnouncingSettlesNothingElse: the settle pass is the same one sync
// runs, so it must not close a step whose predicate is not satisfied.
func TestAnnouncingSettlesNothingElse(t *testing.T) {
	db, key := announceable(t)

	work := addAction(t, db, "--title", "write the endpoint", "--verb", "write")
	if _, err := runCLI(t, "action", "close", "--db", db, work, "--pr", key); err != nil {
		t.Fatalf("action close returned error: %v", err)
	}
	merge := actionWithVerb(t, db, "merge")

	if _, err := runCLI(t, "pr", "announce", "--db", db, key, "--channel", "#reviews"); err != nil {
		t.Fatalf("pr announce returned error: %v", err)
	}

	shown, err := runCLI(t, "action", "show", "--db", db, merge, "-o", "json")
	if err != nil {
		t.Fatalf("action show returned error: %v", err)
	}
	var action map[string]any
	if err := json.Unmarshal([]byte(shown), &action); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if action["state"] == "done" {
		t.Errorf("%s closed on an announcement, but the pull request is not merged", merge)
	}
}

// actionWithVerb returns the id of the one open action using a verb, or "".
func actionWithVerb(t *testing.T, db, verb string) string {
	t.Helper()
	out, err := runCLI(t, "action", "list", "--db", db, "--verb", verb, "-o", "json")
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	var actions []map[string]any
	if err := json.Unmarshal([]byte(out), &actions); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if len(actions) == 0 {
		return ""
	}
	id, _ := actions[0]["id"].(string)
	return id
}

// TestChasesAreCounted is #85's second half: the wait clock reads the
// announcement, so a chase grants more patience — and patience granted for the
// fourth time is not the same situation as the first telling. announced_at is
// overwritten by each one, so nothing else records how many there have been.
func TestChasesAreCounted(t *testing.T) {
	db, key := announceable(t)

	if got := showPRJSON(t, db, key)["announce_count"]; got != float64(0) {
		t.Errorf("announce_count = %#v before announcing, want 0", got)
	}

	for n, at := range []string{"2026-08-01", "2026-08-05", "2026-08-09"} {
		out, err := runCLI(t, "pr", "announce", "--db", db, key,
			"--channel", "#reviews", "--at", at)
		if err != nil {
			t.Fatalf("pr announce returned error: %v", err)
		}
		if want := "announce_count"; !strings.Contains(out, want) {
			t.Errorf("announcing does not report %q:\n%s", want, out)
		}
		if got := showPRJSON(t, db, key)["announce_count"]; got != float64(n+1) {
			t.Errorf("announce_count = %#v after %d announcements, want %d", got, n+1, n+1)
		}
	}
}

// TestRestatingOneAnnouncementIsNotAChase: re-running the command with the
// same instant describes the announcement that already happened, and
// correcting the channel on it is not a second telling. Without --at every
// run is a different instant, which is the ordinary case.
func TestRestatingOneAnnouncementIsNotAChase(t *testing.T) {
	db, key := announceable(t)

	if _, err := runCLI(t, "pr", "announce", "--db", db, key,
		"--channel", "#reviews", "--at", "2026-08-01"); err != nil {
		t.Fatalf("pr announce returned error: %v", err)
	}
	out, err := runCLI(t, "pr", "announce", "--db", db, key,
		"--channel", "#eng", "--at", "2026-08-01")
	if err != nil {
		t.Fatalf("second announce returned error: %v", err)
	}
	if strings.Contains(out, "announce_count") {
		t.Errorf("correcting the channel counted as a chase:\n%s", out)
	}
	if got := showPRJSON(t, db, key)["announce_count"]; got != float64(1) {
		t.Errorf("announce_count = %#v, want 1", got)
	}
}
