package store

import (
	"context"
	"encoding/json"
	"testing"
)

// marshalled encodes a record with its relations and decodes it again, which
// is what both `show` formats do underneath.
func marshalled(t *testing.T, st *Store, r Record) map[string]any {
	t.Helper()
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	encoded, err := tx.MarshalRecord(ctx, r)
	if err != nil {
		t.Fatalf("MarshalRecord() returned error: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("decoding %s: %v", encoded, err)
	}
	return object
}

func stringList(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("%#v is not a list", value)
	}
	out := make([]string, len(items))
	for i, item := range items {
		out[i], _ = item.(string)
	}
	return out
}

// TestActionRelationsReachJSON is SL11: the detail table printed an action's
// blockers and `-o json` silently did not, because a record marshals from its
// own columns and an edge is not one.
func TestActionRelationsReachJSON(t *testing.T) {
	st := newStore(t)

	blocker := addAction(t, st, "write the endpoint", "write")
	first := addAction(t, st, "roll it out", "run")
	second := addAction(t, st, "tell people", "announce")
	blockOn(t, st, first, blocker)
	blockOn(t, st, second, blocker)

	object := marshalled(t, st, blocker)
	if got := stringList(t, object["blocking"]); !equalStrings(got, []string{first.ID, second.ID}) {
		t.Errorf("blocking = %v, want %v", got, []string{first.ID, second.ID})
	}
	if _, present := object["blocked_by"]; present {
		t.Errorf("blocked_by is present on an action with no blockers: %v", object["blocked_by"])
	}

	blocked := marshalled(t, st, first)
	if got := stringList(t, blocked["blocked_by"]); !equalStrings(got, []string{blocker.ID}) {
		t.Errorf("blocked_by = %v, want [%s]", got, blocker.ID)
	}
}

// TestAbsentRelationsAreAbsent: a relation with nothing at the far end is
// left out, not rendered empty. An action with no blockers should not carry
// a key saying so, in either format.
func TestAbsentRelationsAreAbsent(t *testing.T) {
	st := newStore(t)
	a := addAction(t, st, "nothing attached to it", "write")

	object := marshalled(t, st, a)
	for _, name := range []string{"blocked_by", "blocking", "subject_pr", "context_prs"} {
		if _, present := object[name]; present {
			t.Errorf("%s is present with nothing at the far end: %v", name, object[name])
		}
	}
	// The columns are all still there, so this is not an empty object.
	if object["id"] != a.ID {
		t.Errorf("id = %v, want %s", object["id"], a.ID)
	}
}

// TestSubjectAndContextAreSeparateRelations: the schema allows at most one
// subject and closing reads only that, so they are two questions rather than
// one list with a role on each entry.
func TestSubjectAndContextAreSeparateRelations(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	trackRepo(t, st, "scottlaird/roz")
	subject := trackPR(t, st, "scottlaird/roz", 1)
	background := trackPR(t, st, "scottlaird/roz", 2)
	a := addAction(t, st, "write the endpoint", "write")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.LinkPR(ctx, a, subject.ID, RoleSubject); err != nil {
		t.Fatalf("LinkPR(subject) returned error: %v", err)
	}
	if err := tx.LinkPR(ctx, a, background.ID, RoleContext); err != nil {
		t.Fatalf("LinkPR(context) returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	object := marshalled(t, st, a)
	if object["subject_pr"] != subject.ID {
		t.Errorf("subject_pr = %v, want %s", object["subject_pr"], subject.ID)
	}
	if got := stringList(t, object["context_prs"]); !equalStrings(got, []string{background.ID}) {
		t.Errorf("context_prs = %v, want [%s]", got, background.ID)
	}
}

// TestPRRelationsNameTheWork: a tracked pull request gave no clue which piece
// of work it belonged to, which is the direction that was hardest to get at.
func TestPRRelationsNameTheWork(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	trackRepo(t, st, "scottlaird/roz")
	pr := trackPR(t, st, "scottlaird/roz", 1)
	a := addAction(t, st, "write the endpoint", "write")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.LinkPR(ctx, a, pr.ID, RoleSubject); err != nil {
		t.Fatalf("LinkPR() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	object := marshalled(t, st, pr)
	if got := stringList(t, object["actions"]); !equalStrings(got, []string{a.ID}) {
		t.Errorf("actions = %v, want [%s]", got, a.ID)
	}
}

func TestProjectRelationsCarryJira(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	p := insertProject(t, st, "Split the nodepool")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	for _, key := range []string{"CDSS-1744", "CDSS-1801"} {
		if err := tx.LinkProjectJira(ctx, p.ID, key); err != nil {
			t.Fatalf("LinkProjectJira() returned error: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	object := marshalled(t, st, p)
	if got := stringList(t, object["jira"]); !equalStrings(got, []string{"CDSS-1744", "CDSS-1801"}) {
		t.Errorf("jira = %v", got)
	}
}

// TestRecordsWithoutRelationsStillMarshal: most entities declare none, and
// Tx.MarshalRecord has to take them anyway or every caller needs to know
// which is which.
func TestRecordsWithoutRelationsStillMarshal(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	cfg, err := st.Config(ctx)
	if err != nil {
		t.Fatalf("Config() returned error: %v", err)
	}
	object := marshalled(t, st, cfg)
	if object["id"] != ConfigID {
		t.Errorf("id = %v, want %s", object["id"], ConfigID)
	}
}
