package cli

import (
	"strings"
	"testing"
)

// TestProjectParent: a project can be part of another, set either when it is
// created or afterwards, and cleared.
func TestProjectParent(t *testing.T) {
	db := initDB(t)
	parent := addProject(t, db, "Platform")

	// Created with one.
	child := addProject(t, db, "Storage", "--parent", parent)
	if !strings.Contains(showProjectField(t, db, child), parent) {
		t.Errorf("%s was not created under %s", child, parent)
	}

	// Given one afterwards.
	other := addProject(t, db, "Docs")
	if _, err := runCLI(t, "project", "set", "--db", db, other, "--parent", parent); err != nil {
		t.Fatalf("project set --parent returned error: %v", err)
	}
	if !strings.Contains(showProjectField(t, db, other), parent) {
		t.Errorf("%s was not put under %s", other, parent)
	}

	// And cleared.
	out, err := runCLI(t, "project", "set", "--db", db, other, "--parent", "")
	if err != nil {
		t.Fatalf("project set --parent \"\" returned error: %v", err)
	}
	if !strings.Contains(out, "parent_id") {
		t.Errorf("clearing printed %q, want the change", out)
	}
}

// TestAProjectCannotBeItsOwnAncestor covers the whole chain, not just the
// obvious case: a cycle is a property of the walk, and only the one-step
// version is something the schema can refuse.
func TestAProjectCannotBeItsOwnAncestor(t *testing.T) {
	db := initDB(t)
	top := addProject(t, db, "Platform")
	middle := addProject(t, db, "Storage", "--parent", top)
	bottom := addProject(t, db, "Sharding", "--parent", middle)

	// Itself.
	_, err := runCLI(t, "project", "set", "--db", db, top, "--parent", top)
	if err == nil {
		t.Fatal("a project was made its own parent")
	}
	if !strings.Contains(err.Error(), "its own parent") {
		t.Errorf("error = %v", err)
	}

	// Two levels down, which no single-row constraint can see.
	_, err = runCLI(t, "project", "set", "--db", db, top, "--parent", bottom)
	if err == nil {
		t.Fatal("a cycle two levels deep was accepted")
	}
	// The error names the chain, so it is clear which link is the problem.
	for _, want := range []string{bottom, middle, top} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
}

func TestAParentMustExist(t *testing.T) {
	db := initDB(t)
	id := addProject(t, db, "Storage")

	if _, err := runCLI(t, "project", "set", "--db", db, id, "--parent", "ROZ999"); err == nil {
		t.Fatal("a parent that does not exist was accepted")
	}
	if _, err := runCLI(t, "project", "add", "--db", db,
		"--title", "Sharding", "--parent", "ROZ999"); err == nil {
		t.Fatal("a project was created under a parent that does not exist")
	}
}

// TestProjectListIsFlatByDefault: most projects have no parent, and a
// hierarchy of one level is a list with extra ceremony.
func TestProjectListIsFlatByDefault(t *testing.T) {
	db := initDB(t)
	parent := addProject(t, db, "Platform")
	addProject(t, db, "Storage", "--parent", parent)

	out, err := runCLI(t, "project", "list", "--db", db)
	if err != nil {
		t.Fatalf("project list returned error: %v", err)
	}
	if titleColumn(t, out, "Platform") != titleColumn(t, out, "Storage") {
		t.Errorf("the default listing is indented:\n%s", out)
	}
}

// TestProjectListTreeIndents, and keeps the sort within each level: the shape
// is a way of reading the order, not a replacement for it.
func TestProjectListTreeIndents(t *testing.T) {
	db := initDB(t)
	top := addProject(t, db, "Platform", "--priority", "3")
	addProject(t, db, "Zebra", "--parent", top, "--priority", "2")
	addProject(t, db, "Alpha", "--parent", top, "--priority", "1")

	out, err := runCLI(t, "project", "list", "--db", db, "--tree", "--sort", "priority")
	if err != nil {
		t.Fatalf("project list --tree returned error: %v", err)
	}
	parent := titleColumn(t, out, "Platform")
	for _, child := range []string{"Alpha", "Zebra"} {
		if titleColumn(t, out, child) <= parent {
			t.Errorf("%s is not indented under its parent:\n%s", child, out)
		}
	}
	// Alpha is priority 1 and Zebra 2, so the sort still decides among them.
	if strings.Index(out, "Alpha") > strings.Index(out, "Zebra") {
		t.Errorf("the sort was not kept within the level:\n%s", out)
	}
}

// TestAClosedParentHoldsItsChildrenUp: filtering to open work would otherwise
// orphan a child whose parent is finished, which is the opposite of what
// filtering asked for.
func TestAClosedParentHoldsItsChildrenUp(t *testing.T) {
	db := initDB(t)
	top := addProject(t, db, "Platform")
	child := addProject(t, db, "Storage", "--parent", top)

	if _, err := runCLI(t, "project", "close", "--db", db, top); err != nil {
		t.Fatalf("project close returned error: %v", err)
	}

	out, err := runCLI(t, "project", "list", "--db", db, "--tree", "--status", "active")
	if err != nil {
		t.Fatalf("project list --tree returned error: %v", err)
	}
	if !strings.Contains(out, top) {
		t.Errorf("the closed parent is missing, orphaning its child:\n%s", out)
	}
	if !strings.Contains(out, "(closed)") {
		t.Errorf("the closed parent is not marked as context:\n%s", out)
	}
	if !strings.Contains(out, child) {
		t.Errorf("the open child is missing:\n%s", out)
	}
}

// showProjectField returns a project as JSON, for asserting on a column the
// table does not show.
func showProjectField(t *testing.T, db, id string) string {
	t.Helper()
	out, err := runCLI(t, "project", "show", "--db", db, id, "-o", "json")
	if err != nil {
		t.Fatalf("project show returned error: %v", err)
	}
	return out
}

// titleColumn is where a title starts on its line, which is what indentation
// actually means once a tabwriter has padded every column.
//
// Asserting on leading spaces would pass for the wrong reason: the table is
// padded to align, so every title has spaces in front of it whether or not it
// is indented.
func titleColumn(t *testing.T, out, title string) int {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, title); i >= 0 {
			return i
		}
	}
	t.Fatalf("%q is not in the listing:\n%s", title, out)
	return -1
}
