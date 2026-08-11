package cli

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestConcurrentTreesKeepTheirOwnDatabase is the reason --db is bound to the
// command rather than to the package.
//
// `roz mcp` builds a fresh tree per tool call, so two calls in flight mean
// two trees parsing --db at once. While the value lived in a package variable
// that was a write/write race, and the losing tree opened the winner's
// database. Run under -race this fails loudly against that version; run
// without, it still fails, because the rows land in the wrong file.
func TestConcurrentTreesKeepTheirOwnDatabase(t *testing.T) {
	const rounds = 25

	first, second := initDB(t), initDB(t)
	dbs := map[string]string{"first": first, "second": second}

	var wg sync.WaitGroup
	errs := make(chan error, 2*rounds)

	for name, db := range dbs {
		for i := range rounds {
			wg.Add(1)
			go func() {
				defer wg.Done()
				title := fmt.Sprintf("%s-%d", name, i)
				if _, err := runCLI(t, "project", "add", "--db", db, "--title", title); err != nil {
					errs <- fmt.Errorf("adding %s: %w", title, err)
				}
			}()
		}
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	// Each database should hold its own titles and none of the other's. A
	// tree that read the wrong path shows up as both at once: a title missing
	// here and present there.
	for name, db := range dbs {
		out, err := runCLI(t, "project", "list", "--db", db)
		if err != nil {
			t.Fatalf("listing %s: %v", name, err)
		}
		for other := range dbs {
			for i := range rounds {
				title := fmt.Sprintf("%s-%d", other, i)
				got := strings.Contains(out, title)
				if want := other == name; got != want {
					t.Errorf("%s database contains %q = %v, want %v", name, title, got, want)
				}
			}
		}
	}
}

// TestDBFlagDefaultsPerTree checks the flag's default is not shared either: a
// tree that never sees --db must not inherit the last one that did.
func TestDBFlagDefaultsPerTree(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "project", "add", "--db", db, "--title", "explicit"); err != nil {
		t.Fatalf("project add returned error: %v", err)
	}

	root := NewRootCmd()
	got, err := root.PersistentFlags().GetString("db")
	if err != nil {
		t.Fatalf("reading --db: %v", err)
	}
	if want := defaultDBPath(); got != want {
		t.Errorf("fresh tree has --db %q, want the default %q", got, want)
	}
}
