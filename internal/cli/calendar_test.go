package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func addCalendarEntry(t *testing.T, db string, args ...string) string {
	t.Helper()
	out, err := runCLI(t, append([]string{"calendar", "add", "--db", db}, args...)...)
	if err != nil {
		t.Fatalf("calendar add returned error: %v", err)
	}
	return strings.TrimSpace(out)
}

func TestCalendarAddAndList(t *testing.T) {
	db := initDB(t)

	id := addCalendarEntry(t, db, "--kind", "oncall", "--starts", "2026-08-10",
		"--ends", "2026-08-16", "--capacity", "reduced", "--label", "oncall week")
	if want := "oncall-2026-08-10"; id != want {
		t.Errorf("window add printed %q, want the derived id %q", id, want)
	}

	out, err := runCLI(t, "calendar", "list", "--db", db)
	if err != nil {
		t.Fatalf("calendar list returned error: %v", err)
	}
	for _, want := range []string{id, "oncall", "2026-08-16", "reduced", "oncall week"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output does not contain %q:\n%s", want, out)
		}
	}
	// The header says which end is inclusive, because that is the mistake.
	if !strings.Contains(out, "TO (INCL)") {
		t.Errorf("list header does not mark the end inclusive:\n%s", out)
	}
}

func TestCalendarListEmpty(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "calendar", "list", "--db", db)
	if err != nil {
		t.Fatalf("calendar list returned error: %v", err)
	}
	if !strings.Contains(out, "no calendar entries") {
		t.Errorf("empty list printed %q", out)
	}
}

// TestWindowEndIsInclusive is the trap worth a test at this level too: a
// window queried on its last day is still on.
func TestCalendarEndIsInclusive(t *testing.T) {
	db := initDB(t)
	id := addCalendarEntry(t, db, "--kind", "oncall", "--starts", "2026-08-10",
		"--ends", "2026-08-16", "--capacity", "reduced")

	onLastDay, err := runCLI(t, "calendar", "list", "--db", db, "--on", "2026-08-16")
	if err != nil {
		t.Fatalf("calendar list --on returned error: %v", err)
	}
	if !strings.Contains(onLastDay, id) {
		t.Errorf("the last day is not covered:\n%s", onLastDay)
	}

	dayAfter, err := runCLI(t, "calendar", "list", "--db", db, "--on", "2026-08-17")
	if err != nil {
		t.Fatalf("calendar list --on returned error: %v", err)
	}
	if strings.Contains(dayAfter, id) {
		t.Errorf("the day after the end is covered:\n%s", dayAfter)
	}
}

func TestCalendarCapacityIsAnEnum(t *testing.T) {
	db := initDB(t)

	// All three are accepted, because oncall is not a block and PTO is.
	for i, capacity := range []string{"none", "reduced", "full"} {
		starts := []string{"2026-01-01", "2026-02-01", "2026-03-01"}[i]
		addCalendarEntry(t, db, "--kind", "other", "--starts", starts, "--ends", starts,
			"--capacity", capacity)
	}

	_, err := runCLI(t, "calendar", "add", "--db", db, "--kind", "pto",
		"--starts", "2026-04-01", "--ends", "2026-04-01", "--capacity", "true")
	if err == nil {
		t.Fatal("a boolean capacity was accepted, want an error")
	}
	if !strings.Contains(err.Error(), "none, reduced, full") {
		t.Errorf("error = %v, want it to list what is accepted", err)
	}
}

// TestNewKindNeedsNoRelease is the point of dropping the CHECK: a kind
// nobody anticipated is recorded, with a note rather than a refusal, and no
// migration or code change behind it.
func TestNewKindNeedsNoRelease(t *testing.T) {
	db := initDB(t)

	root := NewRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"calendar", "add", "--db", db, "--kind", "sabbatical",
		"--starts", "2027-01-04", "--ends", "2027-03-28", "--capacity", "none"})
	if err := root.Execute(); err != nil {
		t.Fatalf("an unfamiliar kind was refused: %v", err)
	}

	if got := strings.TrimSpace(out.String()); got != "sabbatical-2027-01-04" {
		t.Errorf("printed %q, want the derived id", got)
	}
	if !strings.Contains(errOut.String(), "not one of the usual kinds") {
		t.Errorf("nothing was said about the unfamiliar kind: %q", errOut.String())
	}
	// Said once, not once per code path that looks at it.
	if strings.Count(errOut.String(), "not one of the usual kinds") != 1 {
		t.Errorf("the note was repeated:\n%s", errOut.String())
	}

	listed, err := runCLI(t, "calendar", "list", "--db", db, "--kind", "sabbatical")
	if err != nil {
		t.Fatalf("calendar list --kind returned error: %v", err)
	}
	if !strings.Contains(listed, "sabbatical") {
		t.Errorf("the new kind is not filterable:\n%s", listed)
	}
}

func TestCalendarShowJSON(t *testing.T) {
	db := initDB(t)
	id := addCalendarEntry(t, db, "--kind", "pto", "--starts", "2026-08-24",
		"--ends", "2026-09-04", "--capacity", "none", "--label", "Iceland")

	out, err := runCLI(t, "calendar", "show", "--db", db, id, "-o", "json")
	if err != nil {
		t.Fatalf("calendar show returned error: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(out), &object); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if object["ends_on"] != "2026-09-04" || object["capacity"] != "none" {
		t.Errorf("window = %#v, want the values given", object)
	}
}

func TestCalendarSet(t *testing.T) {
	db := initDB(t)
	id := addCalendarEntry(t, db, "--kind", "oncall", "--starts", "2026-08-10",
		"--ends", "2026-08-16", "--capacity", "reduced")

	out, err := runCLI(t, "calendar", "set", "--db", db, id, "--ends", "2026-08-17")
	if err != nil {
		t.Fatalf("calendar set returned error: %v", err)
	}
	if !strings.Contains(out, "ends_on") || !strings.Contains(out, "2026-08-17") {
		t.Errorf("set output = %q, want the change reported", out)
	}
}

func TestCalendarCustomID(t *testing.T) {
	db := initDB(t)

	id := addCalendarEntry(t, db, "--kind", "pto", "--starts", "2026-08-24",
		"--ends", "2026-09-04", "--capacity", "none", "--id", "iceland")
	if id != "iceland" {
		t.Errorf("window add printed %q, want the given id", id)
	}
}

func TestCalendarDuplicateID(t *testing.T) {
	db := initDB(t)
	addCalendarEntry(t, db, "--kind", "oncall", "--starts", "2026-08-10", "--ends", "2026-08-16")

	_, err := runCLI(t, "calendar", "add", "--db", db, "--kind", "oncall",
		"--starts", "2026-08-10", "--ends", "2026-08-20")
	if err == nil {
		t.Fatal("a second window with the same derived id was accepted, want an error")
	}
	if !strings.Contains(err.Error(), "--id") {
		t.Errorf("error = %v, want it to suggest --id", err)
	}
}

func TestCalendarRejections(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "end before start",
			args:    []string{"--kind", "pto", "--starts", "2026-09-10", "--ends", "2026-09-09"},
			wantErr: "is the last day, inclusive",
		},
		{
			name:    "empty kind",
			args:    []string{"--kind", "", "--starts", "2026-01-01", "--ends", "2026-01-02"},
			wantErr: "cannot be empty",
		},
		{
			name:    "a timestamp is not a date",
			args:    []string{"--kind", "pto", "--starts", "2026-01-01T00:00:00Z", "--ends", "2026-01-02"},
			wantErr: "is not a date",
		},
		{
			name:    "a nonexistent day",
			args:    []string{"--kind", "pto", "--starts", "2026-02-29", "--ends", "2026-03-01"},
			wantErr: "is not a date",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := initDB(t)
			args := append([]string{"calendar", "add", "--db", db}, tt.args...)

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

// TestNoteOnAWindow checks a window is a subject like anything else, so "why
// is this here" has somewhere to live.
func TestNoteOnAWindow(t *testing.T) {
	db := initDB(t)
	id := addCalendarEntry(t, db, "--kind", "oncall", "--starts", "2026-08-10", "--ends", "2026-08-16")

	if _, err := runCLI(t, "note", "--db", db, id, "swapped with Jo"); err != nil {
		t.Fatalf("note on a window returned error: %v", err)
	}

	out, err := runCLI(t, "watch", "--db", db, "--once", "--kind", "note", "--since", "2000-01-01")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if !strings.Contains(out, id) || !strings.Contains(out, "swapped with Jo") {
		t.Errorf("the note is not in the log:\n%s", out)
	}
}
