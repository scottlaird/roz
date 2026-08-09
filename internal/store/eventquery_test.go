package store

import (
	"context"
	"testing"
)

// noisyLog writes a spread of events: three created, one changed, one note
// and one exception.
func noisyLog(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()

	first := insertProject(t, st, "one")
	insertProject(t, st, "two")
	insertProject(t, st, "three")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	after := first.Clone()
	after.Title = "one, renamed"
	if _, err := tx.Update(ctx, first, after); err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if err := tx.Note(ctx, first, "a note"); err != nil {
		t.Fatalf("Note() returned error: %v", err)
	}
	if err := tx.Exception(ctx, first, "unexpected_state", "needs a look"); err != nil {
		t.Fatalf("Exception() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

func eventKinds(events []*Event) []string {
	kinds := make([]string, len(events))
	for i, e := range events {
		kinds[i] = e.Kind
	}
	return kinds
}

func TestEventsReturnsOldestFirst(t *testing.T) {
	st := newStore(t)
	noisyLog(t, st)

	got, err := st.Events(context.Background(), EventQuery{})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	want := []string{"created", "created", "created", "changed", "note", "unexpected_state"}
	if !equalStrings(eventKinds(got), want) {
		t.Errorf("Events() kinds = %v, want %v", eventKinds(got), want)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq <= got[i-1].Seq {
			t.Fatalf("events are not in seq order: %d then %d", got[i-1].Seq, got[i].Seq)
		}
	}
}

// TestEventsNewestStillReturnsOldestFirst is the property the tail depends
// on: the newest n are selected, but printed in the order they happened.
func TestEventsNewestStillReturnsOldestFirst(t *testing.T) {
	st := newStore(t)
	noisyLog(t, st)

	got, err := st.Events(context.Background(), EventQuery{Newest: 3})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	want := []string{"changed", "note", "unexpected_state"}
	if !equalStrings(eventKinds(got), want) {
		t.Errorf("Events(newest 3) kinds = %v, want %v", eventKinds(got), want)
	}
}

func TestEventsBySeverity(t *testing.T) {
	st := newStore(t)
	noisyLog(t, st)

	got, err := st.Events(context.Background(), EventQuery{Severity: SeverityException})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if want := []string{"unexpected_state"}; !equalStrings(eventKinds(got), want) {
		t.Errorf("Events(exception) kinds = %v, want %v", eventKinds(got), want)
	}
}

func TestEventsByKind(t *testing.T) {
	st := newStore(t)
	noisyLog(t, st)

	got, err := st.Events(context.Background(), EventQuery{Kind: eventCreated})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("Events(kind=created) returned %d events, want 3", len(got))
	}
}

// TestEventsAfterSeq is the cursor walk a tail performs between polls.
func TestEventsAfterSeq(t *testing.T) {
	st := newStore(t)
	noisyLog(t, st)

	all, err := st.Events(context.Background(), EventQuery{})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	cursor := all[2].Seq

	got, err := st.Events(context.Background(), EventQuery{AfterSeq: cursor})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if len(got) != len(all)-3 {
		t.Fatalf("Events(after %d) returned %d events, want %d", cursor, len(got), len(all)-3)
	}
	if got[0].Seq != cursor+1 {
		t.Errorf("first event after the cursor has seq %d, want %d", got[0].Seq, cursor+1)
	}
}

func TestEventsAfterSeqAtTailIsEmpty(t *testing.T) {
	st := newStore(t)
	noisyLog(t, st)

	all, err := st.Events(context.Background(), EventQuery{})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}

	got, err := st.Events(context.Background(), EventQuery{AfterSeq: all[len(all)-1].Seq})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Events() past the end returned %d events, want none", len(got))
	}
}

func TestEventsSince(t *testing.T) {
	st := newStore(t)
	noisyLog(t, st)

	// newStore advances the clock a second per transaction, so the first
	// three projects and the later batch have distinct timestamps.
	all, err := st.Events(context.Background(), EventQuery{})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	cutoff := all[3].At

	got, err := st.Events(context.Background(), EventQuery{SinceAt: cutoff})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if len(got) == 0 || len(got) == len(all) {
		t.Fatalf("Events(since %s) returned %d of %d events, want a strict subset", cutoff, len(got), len(all))
	}
	for _, e := range got {
		if e.At < cutoff {
			t.Errorf("event at %s is before the cutoff %s", e.At, cutoff)
		}
	}
}

// TestEventsFiltersCombine checks a filter still applies when the tail is
// walking forward, not just on the backlog.
func TestEventsFiltersCombine(t *testing.T) {
	st := newStore(t)
	noisyLog(t, st)

	got, err := st.Events(context.Background(), EventQuery{Kind: eventCreated, Newest: 1})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if len(got) != 1 || got[0].Kind != eventCreated {
		t.Fatalf("Events(kind=created, newest 1) = %v, want one created event", eventKinds(got))
	}
	if got[0].SubjectID != "SL3" {
		t.Errorf("newest created event is for %s, want SL3", got[0].SubjectID)
	}
}

func TestEventsPopulatesEveryColumn(t *testing.T) {
	st := newStore(t)
	noisyLog(t, st)

	got, err := st.Events(context.Background(), EventQuery{Kind: eventChanged})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d changed events, want 1", len(got))
	}

	e := got[0]
	if e.Seq == 0 || e.At == "" || e.Correlation == "" {
		t.Errorf("event is missing seq, at or correlation: %+v", e)
	}
	if e.Actor != string(ActorHuman) || e.SubjectType != "project" || e.SubjectID != "SL1" {
		t.Errorf("event subject = %s/%s by %s, want project/SL1 by human", e.SubjectType, e.SubjectID, e.Actor)
	}
	if e.Field != "title" || e.OldValue != "one" || e.NewValue != "one, renamed" {
		t.Errorf("event change = %s %q → %q, want title", e.Field, e.OldValue, e.NewValue)
	}
	if e.Payload != "{}" {
		t.Errorf("payload = %q, want {}", e.Payload)
	}
}

func TestEventMarshalsAsJSON(t *testing.T) {
	st := newStore(t)
	noisyLog(t, st)

	got, err := st.Events(context.Background(), EventQuery{Kind: eventChanged})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}

	data, err := MarshalRecord(got[0])
	if err != nil {
		t.Fatalf("MarshalRecord() returned error: %v", err)
	}
	object := decode(t, data)
	if object["field"] != "title" {
		t.Errorf("marshalled event field = %#v, want title", object["field"])
	}
	// payload is format:"json", so it is an object, not a quoted string.
	if _, ok := object["payload"].(map[string]any); !ok {
		t.Errorf("payload = %#v, want an object", object["payload"])
	}
}
