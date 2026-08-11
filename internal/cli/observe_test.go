package cli

import (
	"strings"
	"testing"
)

func TestVerifyStampsAndSays(t *testing.T) {
	db := initDB(t)
	id := addProject(t, db, "Split the nodepool")

	out, err := runCLI(t, "verify", "--db", db, id, "--at", "2026-08-04")
	if err != nil {
		t.Fatalf("verify returned error: %v", err)
	}
	if !strings.Contains(out, id) || !strings.Contains(out, "2026-08-04") {
		t.Errorf("verify did not report what it did:\n%s", out)
	}

	shown, err := runCLI(t, "project", "show", "--db", db, id)
	if err != nil {
		t.Fatalf("project show returned error: %v", err)
	}
	if !strings.Contains(shown, "last_verified_at") || !strings.Contains(shown, "2026-08-04") {
		t.Errorf("the timestamp is not on the record:\n%s", shown)
	}
}

// TestVerifyIsLoggedAsItsOwnActor: the log has to say a person went and
// looked, not that an integration reported it.
func TestVerifyIsLoggedAsItsOwnActor(t *testing.T) {
	db := initDB(t)
	id := addProject(t, db, "Split the nodepool")

	if _, err := runCLI(t, "verify", "--db", db, id, "--at", "2026-08-04"); err != nil {
		t.Fatalf("verify returned error: %v", err)
	}
	out, err := runCLI(t, "watch", "--db", db, "--once")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if !strings.Contains(out, "sync:verify") {
		t.Errorf("the log does not name the verify actor:\n%s", out)
	}
}

// TestVerifyOnAnAction: the column the sketch left off, and the one that
// matters more — an action claims something is worth doing now.
func TestVerifyOnAnAction(t *testing.T) {
	db := initDB(t)
	id := addAction(t, db, "--title", "write the endpoint", "--verb", "write")

	if _, err := runCLI(t, "verify", "--db", db, id); err != nil {
		t.Fatalf("verify returned error: %v", err)
	}
	shown, err := runCLI(t, "action", "show", "--db", db, id)
	if err != nil {
		t.Fatalf("action show returned error: %v", err)
	}
	if !strings.Contains(shown, "last_verified_at") {
		t.Errorf("the action was not stamped:\n%s", shown)
	}
}

func TestVerifyRejections(t *testing.T) {
	db := initDB(t)
	addProject(t, db, "Split the nodepool")
	if _, err := runCLI(t, "repo", "track", "--db", db, "scottlaird/todo"); err != nil {
		t.Fatalf("repo track returned error: %v", err)
	}

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "something with no such timestamp",
			args:    []string{"verify", "scottlaird/todo"},
			wantErr: "cannot be verified",
		},
		{
			name:    "an unknown subject",
			args:    []string{"verify", "TD99"},
			wantErr: "TD99",
		},
		{
			name:    "a date that is not one",
			args:    []string{"verify", "TD1", "--at", "next week"},
			wantErr: "is not a date",
		},
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

func TestSortStaleness(t *testing.T) {
	db := initDB(t)
	recent := addProject(t, db, "checked yesterday")
	old := addProject(t, db, "checked last week")
	never := addProject(t, db, "never checked")

	for id, at := range map[string]string{recent: "2026-08-10", old: "2026-08-04"} {
		if _, err := runCLI(t, "verify", "--db", db, id, "--at", at); err != nil {
			t.Fatalf("verify returned error: %v", err)
		}
	}

	out, err := runCLI(t, "project", "list", "--db", db, "--sort", "staleness")
	if err != nil {
		t.Fatalf("project list --sort staleness returned error: %v", err)
	}
	order := []int{strings.Index(out, never), strings.Index(out, old), strings.Index(out, recent)}
	for i := 1; i < len(order); i++ {
		if order[i-1] > order[i] {
			t.Errorf("want never, then oldest, then most recent:\n%s", out)
			break
		}
	}
}

func TestSortStalenessIsAnAcceptedOrder(t *testing.T) {
	db := initDB(t)
	for _, args := range [][]string{
		{"action", "list", "--sort", "staleness"},
		{"project", "list", "--sort", "staleness"},
	} {
		if _, err := runCLI(t, append(args, "--db", db)...); err != nil {
			t.Errorf("%v returned error: %v", args, err)
		}
	}
	if _, err := runCLI(t, "action", "list", "--db", db, "--sort", "whenever"); err == nil {
		t.Error("an unknown order was accepted")
	}
}
