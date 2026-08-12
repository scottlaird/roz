package codeowners

import "strings"

// Pattern is one CODEOWNERS path pattern, compiled into the form the matcher
// walks.
//
// GitHub's patterns are gitignore's, minus negation with ! and minus the
// [a-z] character ranges. What is left:
//
//   - `*` matches any run of characters within one path segment, never `/`.
//   - `?` matches one character within a segment, never `/`.
//   - `**` matches any number of segments, including none.
//   - A pattern containing no `/` (a trailing one aside) matches by name at
//     any depth: `build` matches `build`, `a/build` and `a/b/build`.
//   - Any other pattern is anchored at the repository root, whether or not it
//     is written with a leading `/`.
//   - A trailing `/` restricts the pattern to directories.
//
// The rule that does the most work here is unwritten in the syntax: owning a
// directory owns everything beneath it. A pattern that matches a *prefix* of a
// file's path therefore matches the file, which is why `docs/` covers
// `docs/a/b.md` and `*` covers the repository.
type Pattern struct {
	// segments is the pattern split on `/`, with any leading slash removed.
	segments []string
	// anchored says the first segment must line up with the repository root.
	anchored bool
	// dirOnly came from a trailing slash: the pattern names a directory, so it
	// can only match a proper prefix of a file's path, never the file itself.
	dirOnly bool
}

// ParsePattern compiles one pattern. It never fails: every string is a legal
// pattern, and one that matches nothing is a question about the CODEOWNERS
// file rather than about this.
func ParsePattern(raw string) Pattern {
	p := Pattern{}

	if strings.HasSuffix(raw, "/") {
		p.dirOnly = true
		raw = strings.TrimSuffix(raw, "/")
	}
	if strings.HasPrefix(raw, "/") {
		p.anchored = true
		raw = strings.TrimPrefix(raw, "/")
	} else if strings.Contains(raw, "/") {
		// gitignore's rule: an interior slash anchors the pattern even without
		// a leading one, so `docs/build` is the one at the root and not any
		// build directory that happens to sit under a docs.
		p.anchored = true
	}

	p.segments = strings.Split(raw, "/")
	return p
}

// Match reports whether the pattern covers path.
//
// path is a repository-relative file path with `/` separators and no leading
// slash — what a pull request's file list holds.
func (p Pattern) Match(path string) bool {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return false
	}
	segments := strings.Split(path, "/")

	if p.anchored {
		return p.matchFrom(segments, 0)
	}
	// Unanchored: the pattern may begin at any segment. Matching at a later
	// index is what makes `build` find `a/b/build`.
	for start := range segments {
		if p.matchFrom(segments, start) {
			return true
		}
	}
	return false
}

// matchFrom walks pattern segments against path segments from start.
//
// A match that consumes every pattern segment succeeds whether or not it
// consumed the whole path: the leftover is what lives inside the directory the
// pattern named, and owning the directory owns that too. dirOnly is the one
// case that insists on leftovers, since a directory pattern cannot be the file
// itself.
func (p Pattern) matchFrom(segments []string, start int) bool {
	if !walk(p.segments, segments[start:]) {
		return false
	}
	if p.dirOnly {
		return consumedPrefix(p.segments, segments[start:])
	}
	return true
}

// consumedPrefix reports whether the pattern could only have matched a proper
// prefix of the path — that is, whether there is anything left to be inside.
//
// `**` makes this inexact: `a/**` can consume everything, so a directory
// pattern ending in `**` is treated as covering whatever is under it, which is
// the reading that matters.
func consumedPrefix(pattern, path []string) bool {
	if len(pattern) > 0 && pattern[len(pattern)-1] == "**" {
		return true
	}
	return len(path) > len(pattern)
}

// walk matches pattern segments against path segments, left to right.
//
// `**` is the only segment that can consume more than one, so it is the only
// place this recurses: try every split and take the first that works.
func walk(pattern, path []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			rest := pattern[1:]
			// A trailing `**` means everything *inside*, so there has to be
			// something inside: `apps/**` covers `apps/a.js` and not the entry
			// `apps` itself. In the middle it may still consume nothing, which
			// is why `a/**/b` matches `a/b`.
			if len(rest) == 0 {
				return len(path) > 0
			}
			for skip := 0; skip <= len(path); skip++ {
				if walk(rest, path[skip:]) {
					return true
				}
			}
			return false
		}
		if len(path) == 0 {
			return false
		}
		if !matchSegment(pattern[0], path[0]) {
			return false
		}
		pattern, path = pattern[1:], path[1:]
	}
	// Every pattern segment matched. Leftover path is the directory's contents.
	return true
}

// matchSegment matches one path segment against one pattern segment, where
// `*` and `?` may not cross a separator — which they cannot here, because a
// segment holds none.
func matchSegment(pattern, name string) bool {
	// Fast path: most segments are literals.
	if !strings.ContainsAny(pattern, "*?") {
		return pattern == name
	}
	return globSegment(pattern, name)
}

// globSegment is `*` and `?` against one segment, iteratively, with
// backtracking on the last `*` seen. Recursion would be shorter and quadratic
// on adversarial patterns; this is linear on the ones people write.
func globSegment(pattern, name string) bool {
	var p, n, star, mark int
	star = -1

	for n < len(name) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == name[n]):
			p++
			n++
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, n
			p++
		case star >= 0:
			// Back up: let the last `*` take one more character.
			p, mark = star+1, mark+1
			n = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
