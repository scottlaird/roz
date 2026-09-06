package store

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestValidatePrefix(t *testing.T) {
	tests := []struct {
		name    string
		prefix  string
		wantErr bool
	}{
		{name: "letters", prefix: "SL"},
		{name: "single letter", prefix: "S"},
		{name: "lowercase", prefix: "sl"},
		{name: "long", prefix: "PROJECT"},
		{name: "empty", prefix: "", wantErr: true},
		{name: "trailing digit", prefix: "S1", wantErr: true},
		{name: "leading digit", prefix: "1S", wantErr: true},
		{name: "hyphen", prefix: "S-", wantErr: true},
		{name: "space", prefix: "S L", wantErr: true},
		{name: "non-ascii letter", prefix: "SÉ", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePrefix(tt.prefix)
			if got := err != nil; got != tt.wantErr {
				t.Errorf("ValidatePrefix(%q) error = %v, want error %v", tt.prefix, err, tt.wantErr)
			}
		})
	}
}

func TestValidatePrefixes(t *testing.T) {
	tests := []struct {
		name     string
		prefixes map[Entity]string
		wantErr  bool
	}{
		{
			name:     "defaults",
			prefixes: map[Entity]string{EntityProject: "SL", EntityAction: "NA"},
		},
		{
			name:     "one a substring of the other stays unambiguous",
			prefixes: map[Entity]string{EntityProject: "S", EntityAction: "SL"},
		},
		{
			name:     "duplicate",
			prefixes: map[Entity]string{EntityProject: "SL", EntityAction: "SL"},
			wantErr:  true,
		},
		{
			name:     "missing entity",
			prefixes: map[Entity]string{EntityProject: "SL"},
			wantErr:  true,
		},
		{
			name:     "invalid prefix",
			prefixes: map[Entity]string{EntityProject: "SL", EntityAction: "N4"},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePrefixes(tt.prefixes)
			if got := err != nil; got != tt.wantErr {
				t.Errorf("validatePrefixes(%v) error = %v, want error %v", tt.prefixes, err, tt.wantErr)
			}
		})
	}
}

func TestInitSeedsSequences(t *testing.T) {
	path := newDBPath(t)
	want := map[Entity]string{EntityProject: "PRJ", EntityAction: "ACT"}

	result, err := Init(context.Background(), path, want)
	if err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}
	if !equalPrefixes(result.Prefixes, want) {
		t.Errorf("Init() prefixes = %v, want %v", result.Prefixes, want)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	got, err := LoadPrefixes(db)
	if err != nil {
		t.Fatalf("LoadPrefixes() returned error: %v", err)
	}
	if !equalPrefixes(got, want) {
		t.Errorf("LoadPrefixes() = %v, want %v", got, want)
	}

	for _, entity := range Entities {
		if got, want := nextN(t, path, entity), firstN; got != want {
			t.Errorf("next_n for %s = %d, want %d", entity, got, want)
		}
	}
}

// TestInitIgnoresRequestedPrefixesOnRerun documents that prefixes are
// write-once: a second Init reports what is stored, not what was asked for.
func TestInitIgnoresRequestedPrefixesOnRerun(t *testing.T) {
	path := newDBPath(t)
	first := map[Entity]string{EntityProject: "SL", EntityAction: "NA"}

	if _, err := Init(context.Background(), path, first); err != nil {
		t.Fatalf("first Init() returned error: %v", err)
	}

	result, err := Init(context.Background(), path, map[Entity]string{EntityProject: "XX", EntityAction: "YY"})
	if err != nil {
		t.Fatalf("second Init() returned error: %v", err)
	}
	if result.Created {
		t.Error("second Init() created = true, want false")
	}
	if !equalPrefixes(result.Prefixes, first) {
		t.Errorf("second Init() prefixes = %v, want the stored %v", result.Prefixes, first)
	}
}

func TestInitRejectsInvalidPrefixes(t *testing.T) {
	path := newDBPath(t)

	_, err := Init(context.Background(), path, map[Entity]string{EntityProject: "SL", EntityAction: "N4"})
	if err == nil {
		t.Fatal("Init() with an invalid prefix returned nil, want an error")
	}
	// Validation runs before anything touches the filesystem.
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("Init() created a database despite invalid prefixes")
	}
}

// TestPrefixImmutability covers both halves of the trigger: it must block a
// rename, and it must not block the allocating UPDATE, which touches only
// next_n.
func TestPrefixImmutability(t *testing.T) {
	path := newDBPath(t)
	if _, err := Init(context.Background(), path, testPrefixes()); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	t.Run("kind cannot be changed", func(t *testing.T) {
		_, err := db.Exec("UPDATE sequence SET kind = 'ZZ' WHERE entity = 'action'")
		if err == nil {
			t.Fatal("renaming a prefix succeeded, want an error")
		}
		if !strings.Contains(err.Error(), "write-once") {
			t.Errorf("renaming a prefix failed with %v, want the trigger's message", err)
		}
	})

	t.Run("entity cannot be changed", func(t *testing.T) {
		if _, err := db.Exec("UPDATE sequence SET entity = 'project' WHERE entity = 'action'"); err == nil {
			t.Error("changing an entity succeeded, want an error")
		}
	})

	t.Run("allocation is unaffected", func(t *testing.T) {
		var n int
		err := db.QueryRow(
			"UPDATE sequence SET next_n = next_n + 1 WHERE entity = 'action' RETURNING next_n - 1").Scan(&n)
		if err != nil {
			t.Fatalf("allocating an identifier: %v", err)
		}
		if want := firstN; n != want {
			t.Errorf("first allocated n = %d, want %d", n, want)
		}
	})
}

func TestLoadPrefixesRejectsMissingRow(t *testing.T) {
	path := newDBPath(t)
	if _, err := Init(context.Background(), path, testPrefixes()); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("DELETE FROM sequence WHERE entity = 'action'"); err != nil {
		t.Fatalf("deleting sequence row: %v", err)
	}
	if _, err := LoadPrefixes(db); err == nil {
		t.Error("LoadPrefixes() with a missing row returned nil, want an error")
	}
}

func equalPrefixes(a, b map[Entity]string) bool {
	if len(a) != len(b) {
		return false
	}
	for entity, prefix := range a {
		if b[entity] != prefix {
			return false
		}
	}
	return true
}
