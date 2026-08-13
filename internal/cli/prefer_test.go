package cli

import (
	"strings"
	"testing"
)

// TestPreferredOwnersAreOrdered: "try these, in this order" is the whole
// content of a preference, so the order has to survive a round trip.
func TestPreferredOwnersAreOrdered(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	out, err := runCLI(t, "repo", "prefer", "acme/api", "--db", db,
		"--prefer", "@org/platform,@org/storage")
	if err != nil {
		t.Fatalf("repo prefer returned error: %v", err)
	}
	if !strings.Contains(out, "@org/platform → @org/storage") {
		t.Errorf("prefer printed %q, want the order", out)
	}
}

// TestPreferredOwnersAreReplaced, not appended to: the list is short and
// ordered, and "the preference is now this" is the only edit anybody makes.
func TestPreferredOwnersAreReplaced(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	if _, err := runCLI(t, "repo", "prefer", "acme/api", "--db", db,
		"--prefer", "@org/platform"); err != nil {
		t.Fatalf("repo prefer returned error: %v", err)
	}
	out, err := runCLI(t, "repo", "prefer", "acme/api", "--db", db, "--prefer", "@org/storage")
	if err != nil {
		t.Fatalf("repo prefer returned error: %v", err)
	}
	if strings.Contains(out, "platform") {
		t.Errorf("prefer printed %q, want the old list gone", out)
	}

	// And clearing them says so rather than printing an empty line.
	out, err = runCLI(t, "repo", "prefer", "acme/api", "--db", db, "--prefer", "")
	if err != nil {
		t.Fatalf("repo prefer returned error: %v", err)
	}
	if !strings.Contains(out, "nobody in particular") {
		t.Errorf("clearing printed %q", out)
	}
}

// TestPreferringIsInTheLog: routing that cannot be accounted for is the
// folklore this replaces, so the preference itself has to be a recorded
// decision with an actor.
func TestPreferringIsInTheLog(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	if _, err := runCLI(t, "repo", "prefer", "acme/api", "--db", db,
		"--prefer", "@org/platform", "--actor", "agent:test"); err != nil {
		t.Fatalf("repo prefer returned error: %v", err)
	}

	out, err := runCLI(t, "watch", "--once", "-n", "50", "--db", db)
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if !strings.Contains(out, "owner_hints") || !strings.Contains(out, "agent:test") {
		t.Errorf("the log does not record the preference:\n%s", out)
	}
}

func TestPreferringAnUntrackedRepositoryIsRefused(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "repo", "prefer", "acme/api", "--db", db, "--prefer", "@org/platform")
	if err == nil {
		t.Fatal("repo prefer accepted an untracked repository")
	}
	if !strings.Contains(err.Error(), "acme/api") {
		t.Errorf("error does not name the repository: %v", err)
	}
}
