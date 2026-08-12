package codeowners

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	const text = `
# Default owners for everything.
*       @org/platform

# Docs are their own thing.
docs/   @org/docs   @alice

# An email address is an owner too.
/legal/ legal@example.com

*.proto  @org/api  # protos are reviewed centrally
`
	file, err := ParseString(text)
	if err != nil {
		t.Fatalf("ParseString() returned error: %v", err)
	}
	if got, want := len(file.Rules), 4; got != want {
		t.Fatalf("parsed %d rules, want %d", got, want)
	}

	for _, tc := range []struct {
		path string
		want []Owner
	}{
		{"main.go", []Owner{"org/platform"}},
		{"docs/a.md", []Owner{"org/docs", "alice"}},
		{"legal/terms.md", []Owner{"legal@example.com"}},
		{"api/v1.proto", []Owner{"org/api"}},
		// The proto rule is last, so it beats the docs rule.
		{"docs/schema.proto", []Owner{"org/api"}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if got := file.Owners(tc.path); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Owners(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestCommentsOnlyCountAtATokenBoundary: `#` starts a comment where a token
// starts, and is an ordinary character inside a pattern. Stripping it
// everywhere would silently truncate a pattern that contains one, and a
// truncated pattern matches the wrong files rather than failing.
func TestCommentsOnlyCountAtATokenBoundary(t *testing.T) {
	file, err := ParseString("a#b/  @org/platform\n")
	if err != nil {
		t.Fatalf("ParseString() returned error: %v", err)
	}
	if got, want := len(file.Rules), 1; got != want {
		t.Fatalf("parsed %d rules, want %d", got, want)
	}
	if got, want := file.Rules[0].Source, "a#b/"; got != want {
		t.Errorf("pattern = %q, want %q", got, want)
	}
	if got := file.Owners("a#b/x.go"); len(got) != 1 {
		t.Errorf("Owners() = %v, want the pattern to have survived", got)
	}
}

func TestParseRecordsWhereARuleCameFrom(t *testing.T) {
	file, err := ParseString("# a comment\n\n*  @org/platform\ndocs/  @org/docs\n")
	if err != nil {
		t.Fatalf("ParseString() returned error: %v", err)
	}

	rule, ok := file.Match("docs/a.md")
	if !ok {
		t.Fatal("no rule matched")
	}
	if rule.Line != 4 || rule.Source != "docs/" {
		t.Errorf("rule = line %d %q, want line 4 %q", rule.Line, rule.Source, "docs/")
	}
}

// TestMatchDistinguishesUnownedFromDisowned. Both mean nobody has to approve,
// but only one of them is a rule somebody wrote, and a caller checking whether
// a path is covered on purpose needs to tell them apart.
func TestMatchDistinguishesUnownedFromDisowned(t *testing.T) {
	file, err := ParseString("/vendor/\n")
	if err != nil {
		t.Fatalf("ParseString() returned error: %v", err)
	}

	if rule, ok := file.Match("vendor/x.go"); !ok || len(rule.Owners) != 0 {
		t.Errorf("Match(vendor) = %v, %v, want a matching rule with no owners", rule, ok)
	}
	if _, ok := file.Match("main.go"); ok {
		t.Error("Match(main.go) found a rule, want none")
	}
}

func TestParseRejectsAnEmptyOwner(t *testing.T) {
	if _, err := ParseString("* @\n"); err == nil {
		t.Fatal("ParseString() accepted a bare @, want an error")
	} else if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error = %v, want it to name the line", err)
	}
}

func TestOwnerRoundTrip(t *testing.T) {
	for _, tc := range []struct{ raw, normalised, written string }{
		{"@org/Platform", "org/platform", "@org/platform"},
		{"@Alice", "alice", "@alice"},
		{"legal@example.com", "legal@example.com", "legal@example.com"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			owner := NormalizeOwner(tc.raw)
			if string(owner) != tc.normalised {
				t.Errorf("NormalizeOwner(%q) = %q, want %q", tc.raw, owner, tc.normalised)
			}
			if got := owner.String(); got != tc.written {
				t.Errorf("String() = %q, want %q", got, tc.written)
			}
		})
	}
}

func TestIsTeam(t *testing.T) {
	if !NormalizeOwner("@org/platform").IsTeam() {
		t.Error("a team was not recognised as one")
	}
	if NormalizeOwner("@alice").IsTeam() {
		t.Error("a user was taken for a team")
	}
}
