package cli

import (
	"strings"
	"testing"
)

// TestOwnerSetAndList: two columns, and both are the point.
func TestOwnerSetAndList(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "owner", "set", "--db", db, "@org/storage",
		"--channel", "#storage-reviews")
	if err != nil {
		t.Fatalf("owner set returned error: %v", err)
	}
	if !strings.Contains(out, "#storage-reviews") {
		t.Errorf("owner set did not report the channel:\n%s", out)
	}

	listed, err := runCLI(t, "owner", "list", "--db", db)
	if err != nil {
		t.Fatalf("owner list returned error: %v", err)
	}
	for _, want := range []string{"@org/storage", "#storage-reviews"} {
		if !strings.Contains(listed, want) {
			t.Errorf("owner list does not mention %q:\n%s", want, listed)
		}
	}
}

// TestOwnerSetRefusesAPerson: a channel is how you reach a group. An
// individual is reached by naming them, and where routing lands on a person
// this has no answer — which is better said than invented.
func TestOwnerSetRefusesAPerson(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "owner", "set", "--db", db, "@alice", "--channel", "#alice")
	if err == nil {
		t.Fatal("owner set accepted a person")
	}
	if !strings.Contains(err.Error(), "person") {
		t.Errorf("error does not say why: %v", err)
	}
}

// TestAnnounceLooksUpTheChannel is the feature: the channel was retyped on
// every invocation, and the lookup has to say what it chose and why, because
// announcing by habit is what it replaces.
func TestAnnounceLooksUpTheChannel(t *testing.T) {
	db, key := trackedPR(t)
	if _, err := runCLI(t, "owner", "set", "--db", db, "@org/storage",
		"--channel", "#storage-reviews"); err != nil {
		t.Fatalf("owner set returned error: %v", err)
	}

	id := addAction(t, db, "--title", "wait for storage", "--verb", "wait_review", "--pr", key)
	if _, err := runCLI(t, "action", "set", "--db", db, id,
		"--waiting-for", "@org/storage"); err != nil {
		t.Fatalf("action set --waiting-for returned error: %v", err)
	}

	out, err := runCLI(t, "pr", "announce", "--db", db, key)
	if err != nil {
		t.Fatalf("pr announce returned error: %v", err)
	}
	if !strings.Contains(out, "#storage-reviews") {
		t.Errorf("announce did not report the channel it used:\n%s", out)
	}
	if !strings.Contains(out, "@org/storage") {
		t.Errorf("announce did not say whose channel it was:\n%s", out)
	}

	// What was recorded is where it went, which is an observation rather than
	// the configuration it came from.
	if got := showPRJSON(t, db, key)["announced_channel"]; got != "#storage-reviews" {
		t.Errorf("announced_channel = %#v, want the channel it used", got)
	}
}

// TestAnnounceSaysWhenNothingKnows: no answer is an answer, and it names what
// it looked at so the fix does not start with reading CODEOWNERS.
func TestAnnounceSaysWhenNothingKnows(t *testing.T) {
	db, key := trackedPR(t)

	_, err := runCLI(t, "pr", "announce", "--db", db, key)
	if err == nil {
		t.Fatal("announce invented a channel")
	}
	if !strings.Contains(err.Error(), "--channel") {
		t.Errorf("error does not offer the way out: %v", err)
	}
}

// TestAnnounceStillTakesAChannel: announcing somewhere unusual is a real thing
// to do, and an explicit channel never has to agree with the configuration.
func TestAnnounceStillTakesAChannel(t *testing.T) {
	db, key := trackedPR(t)
	if _, err := runCLI(t, "owner", "set", "--db", db, "@org/storage",
		"--channel", "#storage-reviews"); err != nil {
		t.Fatalf("owner set returned error: %v", err)
	}

	if _, err := runCLI(t, "pr", "announce", "--db", db, key,
		"--channel", "#incident-room"); err != nil {
		t.Fatalf("pr announce returned error: %v", err)
	}
	if got := showPRJSON(t, db, key)["announced_channel"]; got != "#incident-room" {
		t.Errorf("announced_channel = %#v, want where it actually went", got)
	}
}
