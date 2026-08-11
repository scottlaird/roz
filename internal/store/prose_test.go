package store

import (
	"context"
	"strings"
	"testing"
)

// TestProseRejectsRawHTMLAtInsert: the rule belongs on the path to the
// database, not in each command, so it holds for the CLI, for MCP and for
// anything else that opens a transaction.
func TestProseRejectsRawHTMLAtInsert(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	a := NewAction("write the endpoint", "write")
	a.Why = "unblocks the <b>split</b>"
	if err := st.AllocateAction(ctx, a); err != nil {
		t.Fatalf("AllocateAction() returned error: %v", err)
	}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	err = tx.Insert(ctx, a)
	if err == nil {
		t.Fatal("Insert() accepted raw HTML in why, want an error")
	}
	if !strings.Contains(err.Error(), "action.why") || !strings.Contains(err.Error(), "raw HTML") {
		t.Errorf("error = %v, want it to name the column and the rule", err)
	}
}

func TestProseRejectsRawHTMLAtUpdate(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	before := addAction(t, st, "write the endpoint", "write")

	after := *before
	after.Why = "see <div>the design</div>"

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.Update(ctx, before, &after); err == nil {
		t.Error("Update() accepted raw HTML in why, want an error")
	}
}

// TestProseAcceptsMarkdown: the tag is not a licence to be fussy. Everything
// that is not raw HTML is somebody's prose.
func TestProseAcceptsMarkdown(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	a := NewAction("write the endpoint", "write")
	a.Why = "unblocks the *split*, once `roz sync github` runs — see [the sketch](https://example.com/s)"
	if err := st.AllocateAction(ctx, a); err != nil {
		t.Fatalf("AllocateAction() returned error: %v", err)
	}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.Insert(ctx, a); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}

	// Source in, source out. The store is not a renderer, and the page is the
	// only thing that turns any of this into HTML.
	var got Action
	if err := tx.Load(ctx, &got, a.ID); err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if got.Why != a.Why {
		t.Errorf("why = %q, want the source unchanged: %q", got.Why, a.Why)
	}
}

// TestProseLeavesOtherColumnsAlone: an edit elsewhere must not be refused for
// something already sitting in a prose column, because Update checks only what
// actually moved.
func TestProseLeavesOtherColumnsAlone(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	before := addAction(t, st, "write the endpoint", "write")

	// Reach past the check to plant a value the way a pre-tag database would
	// have held one.
	if _, err := st.db.ExecContext(ctx,
		"UPDATE action SET why = ? WHERE id = ?", "see <b>this</b>", before.ID); err != nil {
		t.Fatalf("planting the value: %v", err)
	}

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	var loaded Action
	if err := tx.Load(ctx, &loaded, before.ID); err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	after := loaded
	after.Title = "write the endpoint properly"

	if _, err := tx.Update(ctx, &loaded, &after); err != nil {
		t.Errorf("Update() of an unrelated column returned error: %v", err)
	}
}
