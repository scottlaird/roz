package codeowners

import "testing"

// TestPatternMatch works through the syntax GitHub documents, case by case.
// The rule doing the most work is unwritten in that syntax: owning a directory
// owns everything beneath it, so a pattern matching a prefix of a path matches
// the file.
func TestPatternMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		path    string
		want    bool
	}{
		// The whole repository.
		{"*", "README.md", true},
		{"*", "a/b/c.go", true},

		// A bare name matches at any depth, file or directory.
		{"build", "build", true},
		{"build", "a/build", true},
		{"build", "a/b/build/out.o", true},
		{"build", "a/rebuild", false},
		{"build", "a/build.go", false},

		// An interior slash anchors, with or without a leading one.
		{"docs/build", "docs/build", true},
		{"docs/build", "a/docs/build", false},
		{"/docs/build", "docs/build", true},
		{"/docs", "docs/a/b.md", true},

		// A trailing slash is directories only.
		{"docs/", "docs/a.md", true},
		{"docs/", "docs/a/b.md", true},
		{"docs/", "docs", false},
		{"build/", "a/build/out.o", true},

		// `*` stops at a separator.
		{"docs/*.md", "docs/a.md", true},
		{"docs/*.md", "docs/a/b.md", false},
		{"*.go", "main.go", true},
		{"*.go", "a/b/main.go", true},
		{"*.go", "main.gox", false},

		// but a `*` that matches a directory still owns what is inside it.
		{"docs/*", "docs/a.md", true},
		{"docs/*", "docs/a/b.md", true},

		// `**` crosses separators, including none at all.
		{"**/logs", "logs", true},
		{"**/logs", "a/logs", true},
		{"**/logs", "a/b/logs/x.txt", true},
		{"apps/**", "apps/a.js", true},
		{"apps/**", "apps/a/b/c.js", true},
		{"apps/**", "apps", false},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/x/y/c", false},

		// `?` is one character, and not a separator.
		{"?.go", "a.go", true},
		{"?.go", "ab.go", false},
		{"a?c/x", "abc/x", true},
		{"a?c/x", "a/c/x", false},

		// A leading slash anchors a bare name that would otherwise float.
		{"/build", "build/out.o", true},
		{"/build", "a/build/out.o", false},

		// Deeper anchored paths.
		{"apps/web/", "apps/web/index.js", true},
		{"apps/web/", "apps/api/index.js", false},
		{"apps/*/index.js", "apps/web/index.js", true},
		{"apps/*/index.js", "apps/web/deep/index.js", false},
	} {
		t.Run(tc.pattern+" ~ "+tc.path, func(t *testing.T) {
			if got := ParsePattern(tc.pattern).Match(tc.path); got != tc.want {
				t.Errorf("ParsePattern(%q).Match(%q) = %v, want %v",
					tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

// TestGlobSegmentBacktracks: a `*` that matched too eagerly has to give
// characters back, which the iterative matcher does by rewinding to the last
// star it saw.
func TestGlobSegmentBacktracks(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"*.go", "a.b.go", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxbyy", false},
		{"*a*a*a", "aaaa", true},
		{"*", "", true},
		{"?", "", false},
	} {
		t.Run(tc.pattern+" ~ "+tc.name, func(t *testing.T) {
			if got := globSegment(tc.pattern, tc.name); got != tc.want {
				t.Errorf("globSegment(%q, %q) = %v, want %v",
					tc.pattern, tc.name, got, tc.want)
			}
		})
	}
}
