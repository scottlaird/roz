package codeowners

import "strings"

// Generated is the linguist-generated marking in a .gitattributes file: the
// paths GitHub treats as generated, and collapses in a pull request's diff.
//
// Generated files are the routine output of a change somewhere else -- a
// regenerated README, compiled protos -- so whoever owns them by CODEOWNERS
// isn't who the change is for.
type Generated struct {
	rules []generatedRule
}

type generatedRule struct {
	pattern   Pattern
	generated bool
}

// ParseGenerated reads the linguist-generated attribute from .gitattributes
// text. Lines that don't mention it are ignored, and so is anything it can't
// read: a quoted pattern, or a macro. An empty file marks nothing.
func ParseGenerated(text string) Generated {
	var g Generated
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") || strings.HasPrefix(fields[0], "\"") {
			continue
		}
		for _, attr := range fields[1:] {
			if set, ok := linguistGenerated(attr); ok {
				g.rules = append(g.rules, generatedRule{pattern: ParsePattern(fields[0]), generated: set})
			}
		}
	}
	return g
}

// linguistGenerated reads one attribute: set, unset with - or !, or given a
// value, true or false.
func linguistGenerated(attr string) (set, ok bool) {
	switch attr {
	case "linguist-generated", "linguist-generated=true":
		return true, true
	case "-linguist-generated", "!linguist-generated", "linguist-generated=false":
		return false, true
	}
	return false, false
}

// Is reports whether path is marked generated. As in git, the last line that
// matches decides.
func (g Generated) Is(path string) bool {
	for i := len(g.rules) - 1; i >= 0; i-- {
		if g.rules[i].pattern.MatchWhole(path) {
			return g.rules[i].generated
		}
	}
	return false
}
