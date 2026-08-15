package filter

import (
	"context"
	"strings"
	"testing"

	"github.com/scottlaird/roz/internal/store"
)

// TestTheFiveShapes is the spike's brief: five questions that cross a
// relation, and what each becomes.
//
// Every one is a correlated EXISTS. That is the finding — a join in a filter
// is always existential, so there is never a join in the SELECT, never a
// duplicated row, and never a DISTINCT to undo one.
func TestTheFiveShapes(t *testing.T) {
	tests := []struct {
		name  string
		blank any
		expr  string
		want  string
	}{
		{
			name:  "a project with a waiting action",
			blank: &store.Project{}, expr: `actions.exists(a, a.verb == "wait_ref")`,
			want: "EXISTS (SELECT 1 FROM action a WHERE a.project_id = project.id AND a.verb = ?)",
		},
		{
			name:  "higher priority than its parent",
			blank: &store.Project{}, expr: `project.priority < parent.priority`,
			want: "EXISTS (SELECT 1 FROM project parent WHERE parent.id = project.parent_id AND project.priority < parent.priority)",
		},
		{
			name:  "a child that outranks it",
			blank: &store.Project{}, expr: `children.exists(c, c.priority < project.priority)`,
			want: "EXISTS (SELECT 1 FROM project c WHERE c.parent_id = project.id AND c.priority < project.priority)",
		},
		{
			name:  "pull requests for one project",
			blank: &store.PR{}, expr: `actions.exists(a, a.project_id == "SL43")`,
			want: "EXISTS (SELECT 1 FROM action a JOIN action_pr j ON j.action_id = a.id WHERE j.pr_id = pr.id AND a.project_id = ?)",
		},
		{
			name:  "a project with no issues",
			blank: &store.Project{}, expr: `issues.size() == 0`,
			want: "NOT EXISTS (SELECT 1 FROM tracker_issue x JOIN project_tracker_issue j ON j.issue_id = x.id WHERE j.project_id = project.id)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Compile(tc.blank, tc.expr)
			if err != nil {
				t.Fatalf("Compile(%q) returned error: %v", tc.expr, err)
			}
			where, _ := f.SQL()
			if want := "(" + tc.want + ")"; where != want {
				t.Errorf("SQL =\n  %s\nwant\n  %s", where, want)
			}
		})
	}
}

// TestABareNameInsideAJoinIsRefused is the divergence that decided how a
// traversal names the outer row.
//
// SQLite resolves an unqualified column against the innermost FROM, so in
// `children.exists(c, c.priority < priority)` the bare name binds to the
// child — making the comparison `c.priority < c.priority`, which matches
// nothing. CEL binds it to the row being filtered. Two meanings, one
// expression, so it does not go to the query.
func TestABareNameInsideAJoinIsRefused(t *testing.T) {
	f, err := Compile(&store.Project{}, `children.exists(c, c.priority < priority)`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	if where, _ := f.SQL(); where != "" {
		t.Errorf("a bare outer reference was pushed down as %q", where)
	}

	// Qualified, it is one query.
	qualified, err := Compile(&store.Project{}, `children.exists(c, c.priority < project.priority)`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	if where, _ := qualified.SQL(); where == "" {
		t.Error("the qualified form did not push down")
	}
}

// TestTheAllowListAppliesInsideAJoinToo: the inner predicate is somebody's
// typing exactly as a top-level one is, and it went to the converter unchecked
// in the first draft of this spike.
//
// A regex is the example now that LIKE is case-sensitive: the SQLite dialect
// refuses matches() outright, so it is a shape that has to run in Go however
// deep it is nested.
func TestTheAllowListAppliesInsideAJoinToo(t *testing.T) {
	f, err := Compile(&store.Project{}, `actions.exists(a, a.verb.matches("^wait"))`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	if where, _ := f.SQL(); where != "" {
		t.Errorf("a regex inside a join was pushed down as %q", where)
	}
}

// countingLoader records what a filter asked for.
type countingLoader struct {
	rows  map[string][]map[string]any
	asked []string
}

func (l *countingLoader) Related(_ context.Context, join store.Join, id string) ([]map[string]any, error) {
	l.asked = append(l.asked, join.Table)
	return l.rows[join.Table], nil
}

// TestARelationIsReadOnlyWhenTheFilterAsksForIt is the lazy half.
//
// The alternative was reading the far side of every relation for every row
// before evaluating anything, which for a filter that mentions one column is
// the whole database to answer a question about a string.
func TestARelationIsReadOnlyWhenTheFilterAsksForIt(t *testing.T) {
	project := &store.Project{ID: "SL1", Title: "a project"}

	// A filter over a column alone reads nothing.
	scalar, err := Compile(&store.Project{}, `title == "a project"`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	loader := &countingLoader{}
	if keep, err := scalar.WithLoader(context.Background(), loader).Keep(project); err != nil || !keep {
		t.Fatalf("Keep() = %v, %v; want true", keep, err)
	}
	if len(loader.asked) != 0 {
		t.Errorf("a scalar filter read %v", loader.asked)
	}

	// A filter that traverses reads that one relation, once, however many
	// times the expression names it.
	traversing, err := Compile(&store.Project{},
		`actions.exists(a, a.verb == "merge") || actions.exists(a, a.verb == "write")`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	loader = &countingLoader{rows: map[string][]map[string]any{
		"action": {{"verb": "write"}},
	}}
	keep, err := traversing.WithLoader(context.Background(), loader).Keep(project)
	if err != nil {
		t.Fatalf("Keep() returned error: %v", err)
	}
	if !keep {
		t.Error("Keep() = false; the project has a write action")
	}
	if len(loader.asked) != 1 || loader.asked[0] != "action" {
		t.Errorf("loader was asked %v, want one read of action", loader.asked)
	}
}

// TestATraversalWithNowhereToReadFromSaysSo: answering "no" because nobody
// wired a loader is indistinguishable from answering "no" because nothing
// matched, and the first is a bug wearing the second's clothes.
func TestATraversalWithNowhereToReadFromSaysSo(t *testing.T) {
	f, err := Compile(&store.Project{}, `actions.exists(a, a.verb.matches("^wait"))`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}

	_, err = f.Keep(&store.Project{ID: "SL1"})
	if err == nil {
		t.Fatal("a traversal with no loader answered rather than failing")
	}
	if !strings.Contains(err.Error(), "actions") {
		t.Errorf("error does not name what it could not read: %v", err)
	}
}

// TestASecondHopIsRefusedRatherThanAnswered is the bug this replaced.
//
// A chain evaluates in Go against a far row that is a map of columns, so
// `a.project` is a missing key, CEL calls that an error, and a row whose
// evaluation fails does not match — every row, silently, giving an empty
// listing indistinguishable from a correct one. Verified against real data
// before the fix: a filter for pull requests whose action belongs to a
// priority-1 project returned nothing, while both halves of it returned rows
// on their own.
func TestASecondHopIsRefusedRatherThanAnswered(t *testing.T) {
	tests := []struct {
		name  string
		blank any
		expr  string
		want  string
	}{
		{
			name:  "a relation of a related record",
			blank: &store.PR{}, expr: `actions.exists(a, a.project.priority == 1)`,
			want: "a.project.priority",
		},
		{
			name:  "a chained to-one",
			blank: &store.Project{}, expr: `parent.parent.title == "x"`,
			want: "parent.parent.title",
		},
		{
			name:  "a traversal inside a traversal",
			blank: &store.Project{}, expr: `children.exists(c, c.actions.exists(a, a.verb == "merge"))`,
			want: "c.actions",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(tc.blank, tc.expr)
			if err == nil {
				t.Fatalf("Compile(%q) answered rather than refusing", tc.expr)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not name the hop: %v", err)
			}
		})
	}
}

// TestARefusalIsNotADemotion: the two are told apart by the compiler, because
// "somewhere else should run this" and "this cannot be answered" look the same
// at the call site and are not the same at all.
func TestARefusalIsNotADemotion(t *testing.T) {
	// Not equivalent: demoted, and the filter still works.
	demoted, err := Compile(&store.Project{}, `actions.exists(a, a.verb.matches("^wait"))`)
	if err != nil {
		t.Fatalf("a demotion became an error: %v", err)
	}
	if where, _ := demoted.SQL(); where != "" {
		t.Error("the demoted term was pushed down after all")
	}

	// Not answerable: refused.
	if _, err := Compile(&store.Project{}, `parent.parent.title == "x"`); err == nil {
		t.Error("an unanswerable filter compiled")
	}
}
