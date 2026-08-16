package cli

import (
	"sort"
	"strings"
	"testing"
)

// TestAViewIsCheckedWhenItIsSaved is the property worth having.
//
// A view is written once and read for months, so the moment to reject one is
// while somebody is still looking at it — not at the render that quietly
// returns nothing.
func TestAViewIsCheckedWhenItIsSaved(t *testing.T) {
	db := initDB(t)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "a column that does not exist",
			args: []string{"--entity", "project", "--filter", `stat == "done"`},
			want: "undeclared reference",
		},
		{
			name: "a listing that does not exist",
			args: []string{"--entity", "widget", "--filter", `x == 1`},
			want: "is not a listing",
		},
		{
			name: "fields the listing does not have",
			args: []string{"--entity", "project", "--fields", "id,nope"},
			want: "not a column",
		},
		{
			name: "a sort the listing does not have",
			args: []string{"--entity", "project", "--sort", "nope"},
			want: "not a column to sort on",
		},
		{
			name: "a filter naming another listing's column",
			args: []string{"--entity", "project", "--filter", `merged_at == null`},
			want: "undeclared reference",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"view", "add", "--db", db, "broken"}, tc.args...)
			_, err := runCLI(t, args...)
			if err == nil {
				t.Fatal("view add accepted it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not say what is wrong: %v", err)
			}
		})
	}
}

// TestAViewNameIsAnIdentifier: a name is an argument, so what it may hold is
// what survives a shell without quoting.
//
// "No spaces" was not enough. A quote, a glob character or an emoji all make a
// name that has to be escaped to be typed, which defeats the point of having
// named it.
func TestAViewNameIsAnIdentifier(t *testing.T) {
	db := initDB(t)

	for _, name := range []string{"stalled", "week_in_review", "q4", "a", "a1_b2"} {
		if _, err := runCLI(t, "view", "add", "--db", db, name,
			"--entity", "project"); err != nil {
			t.Errorf("view add refused %q: %v", name, err)
		}
	}

	for _, name := range []string{
		"Stalled",     // capitals
		"two words",   // a space
		"1st",         // starts with a digit
		"_leading",    // starts with an underscore
		"with-hyphen", // reads as a flag
		"quote'd",     // needs escaping
		"glob*",       // the shell would expand it
		"🎯",           // an emoji
	} {
		if _, err := runCLI(t, "view", "add", "--db", db, name,
			"--entity", "project"); err == nil {
			t.Errorf("view add accepted %q", name)
		}
	}
}

// TestAViewSuppliesDefaults: the view is a starting point, and what somebody
// typed on the line wins over it. A saved answer that ignored the flags beside
// it would be a saved answer nobody trusts.
func TestAViewSuppliesDefaults(t *testing.T) {
	db := initDB(t)
	live := addProject(t, db, "live work")
	addProject(t, db, "also live")
	done := addProject(t, db, "finished")
	if _, err := runCLI(t, "project", "close", "--db", db, done); err != nil {
		t.Fatalf("project close returned error: %v", err)
	}

	if _, err := runCLI(t, "view", "add", "--db", db, "live",
		"--entity", "project", "--filter", `status != "done"`,
		"--fields", "id,title"); err != nil {
		t.Fatalf("view add returned error: %v", err)
	}

	// The view alone.
	out, err := runCLI(t, "project", "list", "--db", db, "--view", "live")
	if err != nil {
		t.Fatalf("project list --view returned error: %v", err)
	}
	if !strings.Contains(out, live) || strings.Contains(out, done) {
		t.Errorf("the view did not select what it says:\n%s", out)
	}

	// An explicit filter replaces the view's, rather than being ANDed with it
	// or ignored.
	narrowed, err := runCLI(t, "project", "list", "--db", db,
		"--view", "live", "--filter", `title == "finished"`, "--fields", "id,title")
	if err != nil {
		t.Fatalf("project list returned error: %v", err)
	}
	if !strings.Contains(narrowed, done) {
		t.Errorf("the typed filter did not win over the view's:\n%s", narrowed)
	}
}

// TestAViewBelongsToOneListing: a project view applied to `pr list` would name
// columns that do not exist there, and the error should say why rather than
// leaving somebody to read a column error.
func TestAViewBelongsToOneListing(t *testing.T) {
	db := initDB(t)
	if _, err := runCLI(t, "view", "add", "--db", db, "live",
		"--entity", "project", "--filter", `status != "done"`); err != nil {
		t.Fatalf("view add returned error: %v", err)
	}

	_, err := runCLI(t, "pr", "list", "--db", db, "--view", "live")
	if err == nil {
		t.Fatal("a project view was applied to pr list")
	}
	if !strings.Contains(err.Error(), "is of project") {
		t.Errorf("error does not say which listing it belongs to: %v", err)
	}
}

// TestShowSaysWhenAViewHasBrokenSince: the columns underneath a view can move
// after it is written, and the failure otherwise looks like an empty listing.
func TestShowSaysWhenAViewHasBrokenSince(t *testing.T) {
	db := initDB(t)
	if _, err := runCLI(t, "view", "add", "--db", db, "live",
		"--entity", "project", "--filter", `status != "done"`); err != nil {
		t.Fatalf("view add returned error: %v", err)
	}

	out, err := runCLI(t, "view", "show", "--db", db, "live")
	if err != nil {
		t.Fatalf("view show returned error: %v", err)
	}
	if !strings.Contains(out, "roz project list --view live") {
		t.Errorf("show does not say how to use it:\n%s", out)
	}
	if strings.Contains(out, "broken") {
		t.Errorf("a working view was reported broken:\n%s", out)
	}
}

// TestDropForgetsTheViewAndNothingElse.
func TestDropForgetsTheViewAndNothingElse(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "live work")
	if _, err := runCLI(t, "view", "add", "--db", db, "live",
		"--entity", "project", "--filter", `status != "done"`); err != nil {
		t.Fatalf("view add returned error: %v", err)
	}

	if _, err := runCLI(t, "view", "drop", "--db", db, "live"); err != nil {
		t.Fatalf("view drop returned error: %v", err)
	}
	if _, err := runCLI(t, "view", "show", "--db", db, "live"); err == nil {
		t.Error("the view survived being dropped")
	}

	out, err := runCLI(t, "project", "list", "--db", db, "--fields", "id")
	if err != nil {
		t.Fatalf("project list returned error: %v", err)
	}
	if !strings.Contains(out, project) {
		t.Errorf("dropping a view took the listing with it:\n%s", out)
	}
}

// TestSeededViewsAreQuestionsRozAlreadyAnswers is the rule 0037 wrote down: a
// seeded view mirrors an existing flag, so the seed set stays definitions
// rather than somebody's working style. It also checks each one still
// compiles, which a hand-written SQL seed cannot check for itself.
func TestSeededViewsAreQuestionsRozAlreadyAnswers(t *testing.T) {
	db := initDB(t)

	for _, tt := range []struct {
		view string
		flag []string
	}{
		{view: "expired_actions", flag: []string{"action", "list", "--expired"}},
		{view: "open_actions", flag: []string{"action", "list", "--open"}},
		{view: "stalled_projects", flag: []string{"project", "list", "--orphaned"}},
		// open_projects is not here: it is seeded under 0036's rule rather
		// than 0037's — the page reads it by name — and `project list` has no
		// --open to compare it against.
	} {
		t.Run(tt.view, func(t *testing.T) {
			entity := tt.flag[0]
			byView, err := runCLI(t, entity, "list", "--db", db, "--view", tt.view, "--fields", "id")
			if err != nil {
				t.Fatalf("%s list --view %s returned error: %v", entity, tt.view, err)
			}
			byFlag, err := runCLI(t, append(append([]string{}, tt.flag...), "--db", db, "--fields", "id")...)
			if err != nil {
				t.Fatalf("%v returned error: %v", tt.flag, err)
			}
			if byView != byFlag {
				t.Errorf("%s and %v select different rows:\n--view:\n%s\n--flag:\n%s",
					tt.view, tt.flag, byView, byFlag)
			}
		})
	}
}

// TestUncheckedActionsIsOpenActionsInAnotherOrder. The one seeded view that is
// not a filter: same rows, sorted by what nobody has looked at. If it ever
// selects a different set, one of the two is wrong.
func TestUncheckedActionsIsOpenActionsInAnotherOrder(t *testing.T) {
	db := initDB(t)

	unchecked, err := runCLI(t, "action", "list", "--db", db, "--view", "unchecked_actions", "--fields", "id")
	if err != nil {
		t.Fatalf("action list --view unchecked_actions returned error: %v", err)
	}
	open, err := runCLI(t, "action", "list", "--db", db, "--view", "open_actions", "--fields", "id")
	if err != nil {
		t.Fatalf("action list --view open_actions returned error: %v", err)
	}
	if sortedLines(unchecked) != sortedLines(open) {
		t.Errorf("unchecked_actions and open_actions select different rows:\n%s\n%s", unchecked, open)
	}
}

func sortedLines(out string) string {
	lines := nonEmptyLines(out)
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
