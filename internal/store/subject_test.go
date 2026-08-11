package store

import (
	"context"
	"testing"
)

func TestSplitIdent(t *testing.T) {
	tests := []struct {
		id         string
		wantPrefix string
		wantN      int64
		wantOK     bool
	}{
		{id: "SL1", wantPrefix: "SL", wantN: 1, wantOK: true},
		{id: "NA103", wantPrefix: "NA", wantN: 103, wantOK: true},
		{id: "S1", wantPrefix: "S", wantN: 1, wantOK: true},
		{id: "PROJECT42", wantPrefix: "PROJECT", wantN: 42, wantOK: true},
		{id: "SL0", wantPrefix: "SL", wantN: 0, wantOK: true},
		{id: "", wantOK: false},
		{id: "SL", wantOK: false},   // no number
		{id: "123", wantOK: false},  // no prefix
		{id: "SL1a", wantOK: false}, // does not end in digits
		// A pull request key: heterogeneous by design, and not a sequence id.
		{id: "myrepo#4174", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			prefix, n, ok := SplitIdent(tt.id)
			if ok != tt.wantOK {
				t.Fatalf("SplitIdent(%q) ok = %v, want %v", tt.id, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if prefix != tt.wantPrefix || n != tt.wantN {
				t.Errorf("SplitIdent(%q) = %q, %d, want %q, %d", tt.id, prefix, n, tt.wantPrefix, tt.wantN)
			}
		})
	}
}

// TestSplitIdentRejectsPRKey is called out separately because subject_id in
// the log is deliberately heterogeneous: it holds SL1 or myrepo#4174, and
// only the first kind is a sequence identifier.
func TestSplitIdentRejectsPRKey(t *testing.T) {
	if _, _, ok := SplitIdent("saas-infra-plane#4174"); ok {
		t.Error("SplitIdent accepted a pull request key, want it rejected")
	}
}

func TestEntityForID(t *testing.T) {
	st := newStore(t)

	tests := []struct {
		id     string
		want   Entity
		wantOK bool
	}{
		{id: "SL1", want: EntityProject, wantOK: true},
		{id: "NA31", want: EntityAction, wantOK: true},
		{id: "SL999", want: EntityProject, wantOK: true},
		{id: "ZZ9", wantOK: false},
		{id: "notanid", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got, ok := st.EntityForID(tt.id)
			if ok != tt.wantOK {
				t.Fatalf("EntityForID(%q) ok = %v, want %v", tt.id, ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("EntityForID(%q) = %s, want %s", tt.id, got, tt.want)
			}
		})
	}
}

// TestEntityForIDFollowsConfiguredPrefixes checks the mapping comes from the
// database rather than from a hardcoded SL, which is the point of storing it.
func TestEntityForIDFollowsConfiguredPrefixes(t *testing.T) {
	path := newDBPath(t)
	if _, err := Init(path, map[Entity]string{EntityProject: "PRJ", EntityAction: "TASK"}); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	st, err := New(db)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	if got, ok := st.EntityForID("PRJ7"); !ok || got != EntityProject {
		t.Errorf("EntityForID(PRJ7) = %s, %v, want project", got, ok)
	}
	if _, ok := st.EntityForID("SL1"); ok {
		t.Error("EntityForID(SL1) resolved against a database that does not use SL")
	}
}

func TestLoadSubject(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := insertProject(t, st, "a title")

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	subject, err := tx.LoadSubject(ctx, st, p.ID)
	if err != nil {
		t.Fatalf("LoadSubject() returned error: %v", err)
	}
	if subject.subjectType() != "project" || subject.subjectID() != p.ID {
		t.Errorf("LoadSubject() = %s/%s, want project/%s",
			subject.subjectType(), subject.subjectID(), p.ID)
	}
}

func TestLoadSubjectUnknownPrefix(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.LoadSubject(ctx, st, "ZZ9"); err == nil {
		t.Error("LoadSubject() with an unknown prefix returned nil, want an error")
	}
}
