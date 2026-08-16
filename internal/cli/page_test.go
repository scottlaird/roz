package cli

import (
	"strings"
	"testing"
)

func TestPageSetAndShow(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "page", "set", "intro", "--db", db,
		"--body", "This week is about the merge queue."); err != nil {
		t.Fatalf("page set returned error: %v", err)
	}
	out, err := runCLI(t, "page", "show", "intro", "--db", db)
	if err != nil {
		t.Fatalf("page show returned error: %v", err)
	}
	if got := strings.TrimSpace(out); got != "This week is about the merge queue." {
		t.Errorf("page show = %q, want the prose as written", got)
	}
}

// TestPageSetReplaces: a slot holds the current text, and what it said before
// is in the log rather than appended to it.
func TestPageSetReplaces(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "page", "set", "intro", "--db", db, "--body", "first"); err != nil {
		t.Fatalf("page set returned error: %v", err)
	}
	out, err := runCLI(t, "page", "set", "intro", "--db", db, "--body", "second")
	if err != nil {
		t.Fatalf("page set returned error: %v", err)
	}
	if !strings.Contains(out, `"first" → "second"`) {
		t.Errorf("page set said %q, want the diff", out)
	}
	if shown, _ := runCLI(t, "page", "show", "intro", "--db", db); strings.Contains(shown, "first") {
		t.Errorf("page show = %q, want only the current text", shown)
	}
}

// TestPageSetRefusesAnUnknownSlot is the point of a closed set: a note filed
// under a key nothing renders would be invisible rather than wrong, and there
// would be no error to say so.
func TestPageSetRefusesAnUnknownSlot(t *testing.T) {
	db := initDB(t)

	_, err := runCLI(t, "page", "set", "sidebar", "--db", db, "--body", "x")
	if err == nil {
		t.Fatal("an unknown slot was accepted")
	}
	if !strings.Contains(err.Error(), "intro") {
		t.Errorf("error = %v, want it to name the slots that do exist", err)
	}
}

// TestPageListShowsEmptySlots: an empty slot is not a gap, it is a place prose
// could go, and listing it is how anyone finds that out.
func TestPageListShowsEmptySlots(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "page", "list", "--db", db)
	if err != nil {
		t.Fatalf("page list returned error: %v", err)
	}
	for _, slot := range []string{"intro", "before-queue", "before-projects", "footer"} {
		if !strings.Contains(out, slot) {
			t.Errorf("page list omits the %s slot:\n%s", slot, out)
		}
	}
}

// TestPageNoteRendersAsMarkdownAndLinks: a note is prose like any other field,
// so it renders as Markdown and links what it names.
func TestPageNoteRendersAsMarkdownAndLinks(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "the target")

	if _, err := runCLI(t, "page", "set", "intro", "--db", db,
		"--body", "This week is **"+project+"**, and not SL999."); err != nil {
		t.Fatalf("page set returned error: %v", err)
	}

	page, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(page, "<strong>") {
		t.Errorf("the note did not render as Markdown:\n%s", page)
	}
	if !strings.Contains(page, `href="#`+project+`"`) {
		t.Errorf("the note did not link what it names:\n%s", page)
	}
	if strings.Contains(page, `href="#SL999"`) {
		t.Errorf("the note linked something that does not exist:\n%s", page)
	}
}

// TestEmptySlotsDrawNothing. A missing key yields a zero struct, and a struct
// is always truthy in a template, so the obvious guard draws an empty box for
// every slot nobody has written to.
func TestEmptySlotsDrawNothing(t *testing.T) {
	db := initDB(t)

	page, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if strings.Contains(page, `class="note"`) {
		t.Errorf("a page with no notes drew a note block:\n%s", page)
	}

	if _, err := runCLI(t, "page", "set", "footer", "--db", db, "--body", "one note"); err != nil {
		t.Fatalf("page set returned error: %v", err)
	}
	page, err = renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if got := strings.Count(page, `class="note"`); got != 1 {
		t.Errorf("%d note blocks for one note, want 1:\n%s", got, page)
	}
}

// TestPageClearEmptiesWithoutDeleting: the row's history is the record of what
// the page used to say.
func TestPageClearEmptiesWithoutDeleting(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "page", "set", "intro", "--db", db, "--body", "temporary"); err != nil {
		t.Fatalf("page set returned error: %v", err)
	}
	if _, err := runCLI(t, "page", "clear", "intro", "--db", db); err != nil {
		t.Fatalf("page clear returned error: %v", err)
	}

	page, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if strings.Contains(page, "temporary") || strings.Contains(page, `class="note"`) {
		t.Errorf("a cleared slot still draws:\n%s", page)
	}

	// The change is in the log, which is where what it used to say lives.
	log, err := runCLI(t, "watch", "--once", "--db", db, "-n", "2")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if !strings.Contains(log, "temporary") {
		t.Errorf("the log does not record what the slot said:\n%s", log)
	}
}

// TestPageNoteRefusesRawHTML: the same rule every other prose field follows,
// enforced by the store rather than by this command.
func TestPageNoteRefusesRawHTML(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "page", "set", "intro", "--db", db,
		"--body", "<script>alert(1)</script>"); err == nil {
		t.Error("raw HTML was accepted into a note")
	}
}
