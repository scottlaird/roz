package filter

import (
	"strings"
	"testing"

	"github.com/scottlaird/roz/internal/store"
)

// TestARelationComparedAsAValueIsRefused is #263. A relation is declared to
// CEL as a dyn, because `subject_pr.state` has to compile and what a far row
// holds is not known until it is read — so `subject_pr == "owner/repo#123"`
// compiles too, and then matches nothing.
//
// An empty result that means "you wrote that wrong" is indistinguishable from
// one that means "there are none", and the second is what a reader believes.
// The reported case was somebody concluding a pull request had no actions
// recorded when it had one, snoozed, with a reason.
func TestARelationComparedAsAValueIsRefused(t *testing.T) {
	for _, expr := range []string{
		`subject_pr == "owner/repo#123"`,
		`subject_pr != "owner/repo#123"`,
		`context_prs == context_prs`,
	} {
		t.Run(expr, func(t *testing.T) {
			_, err := Compile(&store.Action{}, expr)
			if err == nil {
				t.Fatalf("Compile(%q) was accepted, and would have matched nothing", expr)
			}
			// The useful half of the error is which form was meant.
			for _, want := range []string{"is a relation", "<field>"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %v, want it to mention %q", err, want)
				}
			}
		})
	}
}

// TestTheThreeThingsARelationIsForStillCompile. Reading a field, walking it,
// and counting it — including size() in both spellings, since the global one
// works and answers correctly, and refusing it would break a filter that
// already does its job.
func TestTheThreeThingsARelationIsForStillCompile(t *testing.T) {
	for _, expr := range []string{
		`subject_pr.state == "MERGED"`,
		`context_prs.exists(p, p.state == "OPEN")`,
		`context_prs.size() == 0`,
		`size(context_prs) == 0`,
		`subject_pr.state == "MERGED" && context_prs.size() > 1`,
	} {
		t.Run(expr, func(t *testing.T) {
			if _, err := Compile(&store.Action{}, expr); err != nil {
				t.Errorf("Compile(%q) returned error: %v", expr, err)
			}
		})
	}
}

// TestOneRelationNamedTwiceIsJudgedPerUse: `subject_pr.state == subject_pr`
// names it in a legal position and an illegal one, and the illegal one is
// what decides it. Judged by expression id rather than by name, which is why.
func TestOneRelationNamedTwiceIsJudgedPerUse(t *testing.T) {
	if _, err := Compile(&store.Action{}, `subject_pr.state == subject_pr`); err == nil {
		t.Error("a relation compared to one of its own fields was accepted")
	}
	if _, err := Compile(&store.Action{}, `subject_pr.state == subject_pr.state`); err != nil {
		t.Errorf("two legal uses of one relation were refused: %v", err)
	}
}

// TestAMisspelledColumnIsStillCELsError. That half already worked, with a
// caret and a position, and this must not take it over.
func TestAMisspelledColumnIsStillCELsError(t *testing.T) {
	_, err := Compile(&store.Action{}, `titel == "x"`)
	if err == nil {
		t.Fatal("a misspelled column was accepted")
	}
	if !strings.Contains(err.Error(), "undeclared reference") {
		t.Errorf("error = %v, want CEL's own diagnostic", err)
	}
	if strings.Contains(err.Error(), "is a relation") {
		t.Errorf("error = %v, want it not to claim a typo is a relation", err)
	}
}
