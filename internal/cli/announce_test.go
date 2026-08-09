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
