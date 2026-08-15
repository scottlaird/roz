package cli

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/scottlaird/roz/internal/store"
)

// TestWaitingForIsWrittenAsCodeownersWritesIt: one spelling of an owner, so
// that comparing this against CODEOWNERS or against the observed list is a
// comparison that can succeed.
func TestWaitingForIsWrittenAsCodeownersWritesIt(t *testing.T) {
	db, key := trackedPR(t)
	id := addAction(t, db, "--title", "wait for storage", "--verb", "wait_review", "--pr", key)

	if _, err := runCLI(t, "action", "set", "--db", db, id,
		"--waiting-for", "org/storage"); err != nil {
		t.Fatalf("action set --waiting-for returned error: %v", err)
	}

	got := showActionJSON(t, db, id)["waiting_for"]
	if want := "@org/storage"; got != want {
		t.Errorf("waiting_for = %#v, want %q", got, want)
	}
}

// TestWaitingForTakesOneOwner: the column with several owners in it is the
// observed one. Storing a list here would match nothing and look right.
func TestWaitingForTakesOneOwner(t *testing.T) {
	db, key := trackedPR(t)
	id := addAction(t, db, "--title", "wait for somebody", "--verb", "wait_review", "--pr", key)

	_, err := runCLI(t, "action", "set", "--db", db, id,
		"--waiting-for", "@org/storage,@org/platform")
	if err == nil {
		t.Fatal("action set accepted a list of owners")
	}
	if !strings.Contains(err.Error(), "one owner") {
		t.Errorf("error does not say one owner: %v", err)
	}
}

// TestWaitingForClears: a team approves and the wait moves to the next one,
// which happens without the pull request changing at all. Going stale is the
// expected failure, so correcting it has to be cheap.
func TestWaitingForClears(t *testing.T) {
	db, key := trackedPR(t)
	id := addAction(t, db, "--title", "wait for storage", "--verb", "wait_review", "--pr", key)

	for _, args := range [][]string{
		{"--waiting-for", "@org/storage"},
		{"--waiting-for", ""},
	} {
		full := append([]string{"action", "set", "--db", db, id}, args...)
		if _, err := runCLI(t, full...); err != nil {
			t.Fatalf("action set %v returned error: %v", args, err)
		}
	}

	if got := showActionJSON(t, db, id)["waiting_for"]; got != nil {
		t.Errorf("waiting_for = %#v, want null", got)
	}
}

// TestWaitingForColumnAppearsOnlyWhenAnswered: an empty column on every row is
// the listing asking the question rather than answering it. Until CODEOWNERS
// can default this, most actions will not have one.
func TestWaitingForColumnAppearsOnlyWhenAnswered(t *testing.T) {
	db, key := trackedPR(t)
	id := addAction(t, db, "--title", "wait for storage", "--verb", "wait_review", "--pr", key)

	before, err := runCLI(t, "action", "list", "--db", db)
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if strings.Contains(before, "WAITING FOR") {
		t.Errorf("the column showed with nothing in it:\n%s", before)
	}

	if _, err := runCLI(t, "action", "set", "--db", db, id,
		"--waiting-for", "@org/storage"); err != nil {
		t.Fatalf("action set --waiting-for returned error: %v", err)
	}

	after, err := runCLI(t, "action", "list", "--db", db)
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if !strings.Contains(after, "WAITING FOR") || !strings.Contains(after, "@org/storage") {
		t.Errorf("the answer is not in the listing:\n%s", after)
	}
}

// TestTheChipPrefersTheAuthoredOwner: the observed list is every team
// CODEOWNERS pulled in, and reading it as "who are we waiting for" is exactly
// how somebody gets it backwards. Both facts stay true; the page shows the one
// that answers the question, and keeps the date beside it.
func TestTheChipPrefersTheAuthoredOwner(t *testing.T) {
	observed := &store.Action{
		WaitingOn:    `["@org/platform","@org/storage","@org/docs"]`,
		WaitingSince: sql.NullString{String: "2026-08-11T09:00:00.000Z", Valid: true},
	}
	answered := observed.Clone()
	answered.WaitingFor = sql.NullString{String: "@org/storage", Valid: true}

	now := "2026-08-15T00:00:00.000Z"
	if got, want := age(observed, now), "since 2026-08-11"; got != want {
		t.Errorf("age(observed) = %q, want %q", got, want)
	}
	if got, want := age(answered, now), "for @org/storage since 2026-08-11"; got != want {
		t.Errorf("age(answered) = %q, want %q", got, want)
	}
}
