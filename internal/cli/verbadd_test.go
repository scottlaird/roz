package cli

import (
	"strings"
	"testing"
)

// TestVerbAddRefusesWhatWouldStopTheDatabaseOpening is the check the whole
// command exists around.
//
// A verb naming a predicate this build does not register makes OpenStore
// refuse the database — loudly, at startup, for every command afterwards. So
// adding one is a way to make roz stop working entirely, and the registry has
// to be consulted at the only place a verb is written.
func TestVerbAddRefusesWhatWouldStopTheDatabaseOpening(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "verb", "add", "--db", db, "teleport",
		"--closes", "predicate", "--predicate", "pr_teleported", "--rank-class", "click")
	if err == nil {
		t.Fatal("verb add accepted a predicate this build lacks")
	}
	// It says what is registered, so the fix is the next thing you type.
	for _, want := range []string{"pr_teleported", "pr_merged", "stop the database opening"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}

	// And nothing was written: the database still opens.
	if _, err := runCLI(t, "verb", "list", "--db", db); err != nil {
		t.Fatalf("the database no longer opens after a refused verb: %v", err)
	}
}

// TestVerbAddKeepsClosesAndItsPredicateInStep, which is the CHECK the schema
// draws and the pairing the startup check reads.
func TestVerbAddKeepsClosesAndItsPredicateInStep(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "a predicate verb with nothing to close on",
			args:    []string{"nope", "--closes", "predicate", "--rank-class", "click"},
			wantErr: "needs a predicate to close on",
		},
		{
			name: "a human verb naming one",
			args: []string{"nope", "--closes", "human", "--predicate", "pr_merged",
				"--rank-class", "click"},
			wantErr: "may not name a predicate",
		},
		{
			name:    "a rank class nothing styles",
			args:    []string{"nope", "--closes", "human", "--rank-class", "urgent"},
			wantErr: "rank class",
		},
		{
			name:    "a way of closing that is neither",
			args:    []string{"nope", "--closes", "eventually", "--rank-class", "click"},
			wantErr: "which is not human or predicate",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := initDB(t)
			_, err := runCLI(t, append([]string{"verb", "add", "--db", db}, tt.args...)...)
			if err == nil {
				t.Fatalf("verb add %v was accepted", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// TestVerbAddWritesAUsableVerb: added, then used, then still there after the
// store is reopened — which is what says the startup check is satisfied.
func TestVerbAddWritesAUsableVerb(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "verb", "add", "--db", db, "wait_release",
		"--closes", "predicate", "--predicate", "pr_merged", "--rank-class", "wait",
		"--requires-pr", "--label", "wait for a release",
		"--description", "Wait for the release pull request.")
	if err != nil {
		t.Fatalf("verb add returned error: %v", err)
	}
	if !strings.Contains(out, "wait_release") {
		t.Errorf("verb add reported %q", out)
	}

	listed, err := runCLI(t, "verb", "list", "--db", db)
	if err != nil {
		t.Fatalf("verb list returned error: %v", err)
	}
	for _, want := range []string{"wait_release", "pr_merged", "wait"} {
		if !strings.Contains(listed, want) {
			t.Errorf("verb list does not show %q:\n%s", want, listed)
		}
	}

	// A label nobody gives is the verb itself, which is what a row with no
	// friendlier reading already looks like.
	db2 := initDB(t)
	if _, err := runCLI(t, "verb", "add", "--db", db2, "ponder",
		"--closes", "human", "--rank-class", "decide"); err != nil {
		t.Fatalf("verb add returned error: %v", err)
	}
	if listed, _ := runCLI(t, "verb", "list", "--db", db2, "--fields", "verb,label"); !strings.Contains(listed, "ponder") {
		t.Errorf("the defaulted label is missing:\n%s", listed)
	}
}

// TestVerbAddRefusesOneAlreadyThere, and points a retired one at the flag that
// brings it back — rows are never deleted, since closed actions and log
// entries reference retired verbs.
func TestVerbAddRefusesOneAlreadyThere(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "verb", "add", "--db", db, "merge",
		"--closes", "predicate", "--predicate", "pr_merged", "--rank-class", "click")
	if err == nil || !strings.Contains(err.Error(), "already in the vocabulary") {
		t.Fatalf("error = %v, want it to say merge is already there", err)
	}

	if _, err := runCLI(t, "verb", "set", "--db", db, "merge", "--active=false"); err != nil {
		t.Fatalf("verb set --active=false returned error: %v", err)
	}
	_, err = runCLI(t, "verb", "add", "--db", db, "merge",
		"--closes", "predicate", "--predicate", "pr_merged", "--rank-class", "click")
	if err == nil || !strings.Contains(err.Error(), "--active") {
		t.Errorf("error = %v, want it to name the flag that brings a retired verb back", err)
	}
}

// TestWaitMergeIsSeededAndWaits is the verb #241 was actually about: the
// upstream merge you cannot influence, which `merge` described wrongly in both
// of the two settings that matter.
func TestWaitMergeIsSeededAndWaits(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "verb", "list", "--db", db,
		"--fields", "verb,predicate_key,rank_class,wait_days,requires_pr")
	if err != nil {
		t.Fatalf("verb list returned error: %v", err)
	}
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "wait_merge") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("wait_merge is not in the seeded vocabulary:\n%s", out)
	}
	if !strings.Contains(line, "pr_merged") || !strings.Contains(line, "wait") {
		t.Errorf("wait_merge = %q, want it to close on pr_merged in the wait class", line)
	}
	// No allowance, for wait_ref's reason: their merge is not yours to
	// influence and there is nobody to chase, so an exception about it would
	// be a durable item nothing can clear.
	if fields := strings.Fields(line); fields[len(fields)-2] != "-" {
		t.Errorf("wait_merge = %q, want no wait_days", line)
	}
}
