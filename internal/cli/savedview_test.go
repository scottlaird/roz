package cli

import (
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

// TestAViewNameIsAnArgument: one needing quotes is one nobody types twice.
func TestAViewNameIsAnArgument(t *testing.T) {
	db := initDB(t)

	for _, name := range []string{"Stalled", "two words"} {
		_, err := runCLI(t, "view", "add", "--db", db, name, "--entity", "project")
		if err == nil {
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
