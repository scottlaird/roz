// Package codeowners reads a CODEOWNERS file and answers who is required to
// approve a change.
//
// The parsing is the easy half. The useful half is the reduction: a pull
// request touching thirty files under a dozen rules does not want a list of
// every owner mentioned, it wants to know whether one person can approve the
// whole thing, and once somebody has, which of the remaining owners would
// actually help.
package codeowners

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Rule is one line of a CODEOWNERS file.
//
// Owners may be empty. That is not a malformed line: a pattern with no owners
// removes ownership from the paths it matches, which is how a directory is
// carved out of a broader rule above it.
type Rule struct {
	Pattern Pattern
	Owners  []Owner
	// Source is the pattern as written, for error messages and for showing a
	// person which line decided something.
	Source string
	Line   int
}

// Owner is a CODEOWNERS owner: a user, a team, or an email address.
//
// Held normalised — no leading `@`, lower case — because GitHub logins are
// case-insensitive and the same team is written `@Org/Platform` in one file
// and `@org/platform` in another. Comparing raw strings would make those two
// different owners.
type Owner string

// NormalizeOwner puts an owner reference into the form Owner holds.
func NormalizeOwner(raw string) Owner {
	return Owner(strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "@")))
}

// IsTeam reports whether the owner is a team rather than a person. Teams carry
// an org: `org/platform`.
//
// Nothing in this package branches on it — a team and a user are both just
// owners here — but a caller resolving membership needs to know which is
// which, since only a team has members.
func (o Owner) IsTeam() bool { return strings.Contains(string(o), "/") }

// String returns the owner as it would be written, with the `@` back.
func (o Owner) String() string {
	if strings.Contains(string(o), "@") { // an email address
		return string(o)
	}
	return "@" + string(o)
}

// File is a parsed CODEOWNERS file.
type File struct {
	Rules []Rule
}

// Parse reads a CODEOWNERS file.
//
// Unparseable lines are an error rather than a skip. A CODEOWNERS file decides
// who has to approve a change, and quietly ignoring a line means quietly
// deciding that nobody owns something.
func Parse(r io.Reader) (*File, error) {
	file := &File{}
	scanner := bufio.NewScanner(r)

	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(stripComment(scanner.Text()))
		if text == "" {
			continue
		}

		fields := strings.Fields(text)
		rule := Rule{
			Pattern: ParsePattern(fields[0]),
			Source:  fields[0],
			Line:    line,
		}
		for _, raw := range fields[1:] {
			owner := NormalizeOwner(raw)
			if owner == "" {
				return nil, fmt.Errorf("line %d: empty owner", line)
			}
			rule.Owners = append(rule.Owners, owner)
		}
		file.Rules = append(file.Rules, rule)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading CODEOWNERS: %w", err)
	}
	return file, nil
}

// ParseString is Parse over a string, which is what tests and a file already
// read into memory both have.
func ParseString(text string) (*File, error) {
	return Parse(strings.NewReader(text))
}

// stripComment removes a trailing comment. `#` is only a comment at the start
// of a token, so a pattern may contain one.
func stripComment(line string) string {
	if i := strings.Index(line, "#"); i == 0 {
		return ""
	} else if i > 0 && (line[i-1] == ' ' || line[i-1] == '\t') {
		return line[:i]
	}
	return line
}

// Match returns the rule that decides a path's owners, and whether any did.
//
// Last match wins, which is CODEOWNERS and not gitignore: only one rule
// applies to a file, and it is the last one in the file that matches. A rule
// early in the file is a default that anything below it may override.
func (f *File) Match(path string) (Rule, bool) {
	for i := len(f.Rules) - 1; i >= 0; i-- {
		if f.Rules[i].Pattern.Match(path) {
			return f.Rules[i], true
		}
	}
	return Rule{}, false
}

// Owners returns who must approve a change to path.
//
// Empty means nobody in particular: either no rule matched, or the last one
// that did named no owners. Those are different facts — Match distinguishes
// them — but they have the same consequence, which is that this file cannot
// block a merge.
func (f *File) Owners(path string) []Owner {
	rule, ok := f.Match(path)
	if !ok {
		return nil
	}
	return rule.Owners
}
