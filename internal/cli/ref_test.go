package cli

import (
	"strings"
	"testing"
)

// TestWaitRefRefusesAVerbWithNothingToWaitFor: a wait_ref action that never
// says which ref would sit in the queue forever, so the add is refused with
// the flags that fix it.
func TestWaitRefRefusesAVerbWithNothingToWaitFor(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "action", "add", "--db", db,
		"--title", "Wait for the next release", "--verb", "wait_ref")
	if err == nil {
		t.Fatal("action add accepted a wait_ref with nothing to wait for")
	}
	for _, want := range []string{"ref-repo", "ref"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name --%s: %v", want, err)
		}
	}
}

// TestWaitRefNeedsBothHalves: a repository with nothing to wait for would
// match any ref at all, which is never what anybody meant.
func TestWaitRefNeedsBothHalves(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	_, err := runCLI(t, "action", "add", "--db", db,
		"--title", "Wait", "--verb", "wait_ref", "--ref-repo", "acme/api")
	if err == nil {
		t.Fatal("action add accepted a repository with nothing to wait for")
	}
	if !strings.Contains(err.Error(), "go together") {
		t.Errorf("error does not explain the pairing: %v", err)
	}
}

func TestAddAWaitAndSeeIt(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	id := addAction(t, db, "--title", "Wait for the next release", "--verb", "wait_ref",
		"--ref-repo", "acme/api", "--ref", ">=1.5")

	out, err := runCLI(t, "action", "list", "--db", db)
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if !strings.Contains(out, id) {
		t.Errorf("the action is not in the queue:\n%s", out)
	}

	// Nothing has been polled, so there is nothing to list yet — and the
	// listing has to say so rather than looking like an error.
	if _, err := runCLI(t, "ref", "list", "--db", db); err != nil {
		t.Fatalf("ref list returned error: %v", err)
	}
}

// TestWaitRefCorrectsAMistypedExpression: the wait is set on an existing
// action, which is the path `action wait-ref` exists for.
func TestWaitRefCorrectsAMistypedExpression(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	id := addAction(t, db, "--title", "Wait for the next release", "--verb", "wait_ref",
		"--ref-repo", "acme/api", "--ref", ">=1.4")

	out, err := runCLI(t, "action", "wait-ref", "--db", db, "--action", id,
		"--ref-repo", "acme/api", "--ref", "api/>=3.6")
	if err != nil {
		t.Fatalf("action wait-ref returned error: %v", err)
	}
	if want := "acme/api tag api/>=3.6"; !strings.Contains(out, want) {
		t.Errorf("wait-ref printed %q, want it to name %q", out, want)
	}
}

// TestWaitRefRefusesAnUntrackedRepository: a wait against a repository roz
// does not track would never be polled, so it is refused where it is written
// rather than being silently inert.
func TestWaitRefRefusesAnUntrackedRepository(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "action", "add", "--db", db,
		"--title", "Wait", "--verb", "wait_ref",
		"--ref-repo", "acme/api", "--ref", ">=1.5")
	if err == nil {
		t.Fatal("action add accepted a wait on an untracked repository")
	}
	if !strings.Contains(err.Error(), "acme/api") {
		t.Errorf("error does not name the repository: %v", err)
	}
}

// TestWaitRefRefusesAnUnknownKind: branch or tag, named in the error rather
// than surfaced as a CHECK constraint.
func TestWaitRefRefusesAnUnknownKind(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	_, err := runCLI(t, "action", "add", "--db", db,
		"--title", "Wait", "--verb", "wait_ref",
		"--ref-repo", "acme/api", "--ref", ">=1.5", "--ref-kind", "note")
	if err == nil {
		t.Fatal("action add accepted a ref kind that is not a branch or a tag")
	}
	if !strings.Contains(err.Error(), "branch") {
		t.Errorf("error does not name the alternatives: %v", err)
	}
}

// TestWaitRefRefusesAPathWithNothingToMatch: `api/` names a series and says
// nothing about which release, so it can never be satisfied.
func TestWaitRefRefusesAPathWithNothingToMatch(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	_, err := runCLI(t, "action", "add", "--db", db,
		"--title", "Wait", "--verb", "wait_ref",
		"--ref-repo", "acme/api", "--ref", "api/")
	if err == nil {
		t.Fatal("action add accepted a path with nothing to match")
	}
	if !strings.Contains(err.Error(), "nothing to match") {
		t.Errorf("error does not say what is missing: %v", err)
	}
}

// TestAWaitOnAMonorepoSeries: the shape the path prefix exists for, end to end
// through the command.
func TestAWaitOnAMonorepoSeries(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")

	id := addAction(t, db, "--title", "Wait for the next s3 release", "--verb", "wait_ref",
		"--ref-repo", "acme/api", "--ref", "service/s3/>=1.107")

	out, err := runCLI(t, "action", "wait-ref", "--db", db, "--action", id,
		"--ref-repo", "acme/api", "--ref", "service/s3/>=1.108")
	if err != nil {
		t.Fatalf("action wait-ref returned error: %v", err)
	}
	if want := "service/s3/>=1.108"; !strings.Contains(out, want) {
		t.Errorf("wait-ref printed %q, want it to name %q", out, want)
	}
}
