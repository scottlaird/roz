package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/scottlaird/roz/internal/store"
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

// TestPageListAgreesWithItselfAcrossFormats is what #192 was really about: the
// table listed every slot and -o json listed only the written ones, so the two
// formats disagreed about what this listing contains.
func TestPageListAgreesWithItselfAcrossFormats(t *testing.T) {
	db := initDB(t)
	if _, err := runCLI(t, "page", "set", "intro", "--db", db, "--body", "Something."); err != nil {
		t.Fatalf("page set returned error: %v", err)
	}

	table, err := runCLI(t, "page", "list", "--db", db)
	if err != nil {
		t.Fatalf("page list returned error: %v", err)
	}
	shown, err := runCLI(t, "page", "list", "--db", db, "-o", "json")
	if err != nil {
		t.Fatalf("page list -o json returned error: %v", err)
	}

	var slots []struct {
		Key       string `json:"key"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal([]byte(shown), &slots); err != nil {
		t.Fatalf("page list -o json returned %q: %v", shown, err)
	}
	if len(slots) != len(store.Slots) {
		t.Fatalf("-o json listed %d slots, want all %d", len(slots), len(store.Slots))
	}
	for i, slot := range store.Slots {
		if slots[i].Key != slot {
			t.Errorf("slot %d = %q, want %q: the order is where they sit on the page", i, slots[i].Key, slot)
		}
		if !strings.Contains(table, slot) {
			t.Errorf("the table omits %s:\n%s", slot, table)
		}
	}

	// A slot nobody has written to says so with empty columns rather than by
	// being absent — created_at included, which is how the JSON says what the
	// table says with a dash.
	for _, slot := range slots[1:] {
		if slot.Body != "" || slot.CreatedAt != "" {
			t.Errorf("%s was never written but reads as %+v", slot.Key, slot)
		}
	}
}

// TestPageListTakesTheListingFlags. It was the last listing hand-building its
// own table, which is why it had none of these.
func TestPageListTakesTheListingFlags(t *testing.T) {
	db := initDB(t)
	if _, err := runCLI(t, "page", "set", "intro", "--db", db,
		"--body", "The **queue** for this week.\n\nSecond paragraph."); err != nil {
		t.Fatalf("page set returned error: %v", err)
	}

	t.Run("fields", func(t *testing.T) {
		out, err := runCLI(t, "page", "list", "--db", db, "--fields", "key,body")
		if err != nil {
			t.Fatalf("page list --fields returned error: %v", err)
		}
		if strings.Contains(out, "FIRST LINE") {
			t.Errorf("--fields did not narrow the columns:\n%s", out)
		}
		// A note is Markdown and can be paragraphs of it. A cell with a
		// newline in it is not a cell.
		if got := len(nonEmptyLines(out)); got != 5 {
			t.Errorf("got %d lines, want a header and one per slot:\n%s", got, out)
		}
	})

	t.Run("csv", func(t *testing.T) {
		out, err := runCLI(t, "page", "list", "--db", db, "-o", "csv")
		if err != nil {
			t.Fatalf("page list -o csv returned error: %v", err)
		}
		if !strings.HasPrefix(out, "key,updated_at,first_line") {
			t.Errorf("csv header = %q", strings.SplitN(out, "\n", 2)[0])
		}
	})

	t.Run("filter", func(t *testing.T) {
		out, err := runCLI(t, "page", "list", "--db", db, "--filter", `body != ""`, "--fields", "key")
		if err != nil {
			t.Fatalf("page list --filter returned error: %v", err)
		}
		if got := len(nonEmptyLines(out)); got != 2 {
			t.Errorf("got %d lines, want the header and the one written slot:\n%s", got, out)
		}
		if !strings.Contains(out, "intro") {
			t.Errorf("the filter kept the wrong slot:\n%s", out)
		}
	})

	// No --sort, deliberately: the rows are the slots and the order is where
	// they sit on the page, which is the only order that means anything.
	t.Run("no sort", func(t *testing.T) {
		_, err := runCLI(t, "page", "list", "--db", db, "--sort", "key")
		if err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("page list --sort returned %v, want an unknown-flag error", err)
		}
	})
}

// TestPageListKeepsTheSourceInMachineFormats: the one-line body is a rendering
// for the table. Anything reading json or csv wants what was written.
func TestPageListKeepsTheSourceInMachineFormats(t *testing.T) {
	db := initDB(t)
	body := "The **queue** for this week.\n\nSecond paragraph."
	if _, err := runCLI(t, "page", "set", "intro", "--db", db, "--body", body); err != nil {
		t.Fatalf("page set returned error: %v", err)
	}

	shown, err := runCLI(t, "page", "list", "--db", db, "--fields", "key,body", "-o", "json")
	if err != nil {
		t.Fatalf("page list -o json returned error: %v", err)
	}
	var slots []struct {
		Key  string `json:"key"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(shown), &slots); err != nil {
		t.Fatalf("page list -o json returned %q: %v", shown, err)
	}
	if slots[0].Body != body {
		t.Errorf("body = %q, want the source as written", slots[0].Body)
	}
}
