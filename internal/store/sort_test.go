package store

import (
	"strings"
	"testing"
)

// TestNewSortChecksTheColumnsExist is what keeps a column name out of an
// ORDER BY unchecked. SQLite cannot bind one as a parameter, so this is the
// only thing between a caller's string and the query.
func TestNewSortChecksTheColumnsExist(t *testing.T) {
	if _, err := NewSort(&GitRef{}, []SortKey{{Column: "name"}}); err != nil {
		t.Errorf("NewSort() refused a real column: %v", err)
	}

	for _, column := range []string{
		"nope",
		"name; DROP TABLE git_ref",
		"name DESC",
		"1",
		"",
	} {
		_, err := NewSort(&GitRef{}, []SortKey{{Column: column}})
		if err == nil {
			t.Errorf("NewSort() accepted %q", column)
			continue
		}
		// And says what there was, rather than only that this was not it.
		if !strings.Contains(err.Error(), "commit_sha") {
			t.Errorf("error for %q does not name the columns: %v", column, err)
		}
	}
}

func TestSortSQL(t *testing.T) {
	tests := []struct {
		name   string
		keys   []SortKey
		prefix string
		want   string
	}{
		{
			name: "nothing asked for",
			want: "",
		},
		{
			// NULLs last whichever way the key runs. SQLite leads with them
			// ascending, which means "sort by when it merged" would open with
			// everything that never did.
			name: "one key, nulls last",
			keys: []SortKey{{Column: "name"}},
			want: "name IS NULL, name",
		},
		{
			name: "descending, still nulls last",
			keys: []SortKey{{Column: "name", Desc: true}},
			want: "name IS NULL, name DESC",
		},
		{
			name: "primary and tie-breaker, in the order given",
			keys: []SortKey{{Column: "kind"}, {Column: "name", Desc: true}},
			want: "kind IS NULL, kind, name IS NULL, name DESC",
		},
		{
			// The action listing aliases its table, because the ranking joins
			// project and both have a snooze_until.
			name:   "qualified where the query aliases",
			keys:   []SortKey{{Column: "name"}},
			prefix: "a.",
			want:   "a.name IS NULL, a.name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sort, err := NewSort(&GitRef{}, tt.keys)
			if err != nil {
				t.Fatalf("NewSort() returned error: %v", err)
			}
			if got := sort.SQL(tt.prefix); got != tt.want {
				t.Errorf("SQL(%q) = %q, want %q", tt.prefix, got, tt.want)
			}
			if got := sort.Empty(); got != (len(tt.keys) == 0) {
				t.Errorf("Empty() = %v with %d keys", got, len(tt.keys))
			}
		})
	}
}

// TestSortCannotBeBuiltAroundNewSort: the keys are unexported, so the only
// Sort with anything in it is one whose columns were checked. This is a
// compile-time property, and the test is here to say so — a Sort literal with
// keys in it does not build.
func TestSortCannotBeBuiltAroundNewSort(t *testing.T) {
	var hand Sort
	if !hand.Empty() {
		t.Error("the zero Sort is not empty")
	}
	if got := hand.SQL(""); got != "" {
		t.Errorf("the zero Sort renders %q, want nothing", got)
	}
}
