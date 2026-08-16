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
			// Two relations of the same name: each level would alias the same
			// table, and `parent.id = parent.parent_id` is SQL that runs and
			// compares a row to itself.
			name:  "a chained to-one",
			blank: &store.Project{}, expr: `parent.parent.title == "x"`,
			want: "parent.parent.title",
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

// TestTheJunctionCanBeNarrowed: action_pr holds both the subject pull request
// and the context ones, so the two are different relations over one table and
// the junction has to say which rows it means.
func TestTheJunctionCanBeNarrowed(t *testing.T) {
	f, err := Compile(&store.Action{}, `subject_pr.state == "MERGED"`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}

	where, args := f.SQL()
	if !strings.Contains(where, "j.role = ?") {
		t.Errorf("SQL does not narrow the junction:\n  %s", where)
	}
	if len(args) != 2 || args[0] != store.RoleSubject || args[1] != "MERGED" {
		t.Errorf("args = %v, want the role before the predicate's value", args)
	}
}

// TestASetIsOneQuery: `state in [...]` is how anybody would write the filter
// this experiment started from.
func TestASetIsOneQuery(t *testing.T) {
	f, err := Compile(&store.PR{}, `state in ["OPEN", "MERGED"]`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	if where, _ := f.SQL(); where == "" {
		t.Error("a set membership test did not push down")
	}

	// A set of the wrong type does not: SQLite compares across types by
	// affinity where CEL calls it an error.
	mixed, err := Compile(&store.PR{}, `state in ["OPEN", 3]`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	if where, _ := mixed.SQL(); where != "" {
		t.Errorf("a mixed set was pushed down as %q", where)
	}
}

// TestAChainBecomesNestedExists is #200: two hops, and the second is where the
// useful questions are. "PRs for SL41" names a thing; "PRs for anything
// urgent" names a property, and that is the one somebody asks every morning.
func TestAChainBecomesNestedExists(t *testing.T) {
	tests := []struct {
		name  string
		blank any
		expr  string
		want  []string
	}{
		{
			name:  "a to-one beyond a to-many",
			blank: &store.PR{},
			expr:  `actions.exists(a, a.project.priority == 1)`,
			want: []string{
				"EXISTS (SELECT 1 FROM action a JOIN action_pr j ON j.action_id = a.id",
				"j.pr_id = pr.id",
				"EXISTS (SELECT 1 FROM project project WHERE project.id = a.project_id",
				"project.priority = ?",
			},
		},
		{
			name:  "a to-many beyond a to-many",
			blank: &store.Project{},
			expr:  `children.exists(c, c.actions.exists(a, a.verb == "merge"))`,
			want: []string{
				"EXISTS (SELECT 1 FROM project c WHERE c.parent_id = project.id",
				"EXISTS (SELECT 1 FROM action a WHERE a.project_id = c.id",
				"a.verb = ?",
			},
		},
		{
			name:  "a column and a chain in one predicate",
			blank: &store.PR{},
			expr:  `actions.exists(a, a.verb == "merge" && a.project.priority == 1)`,
			want: []string{
				"a.verb = ?",
				"EXISTS (SELECT 1 FROM project project WHERE project.id = a.project_id",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := Compile(tt.blank, tt.expr)
			if err != nil {
				t.Fatalf("Compile(%q) returned error: %v", tt.expr, err)
			}
			where, _ := f.SQL()
			for _, want := range tt.want {
				if !strings.Contains(where, want) {
					t.Errorf("SQL is missing %q:\n%s", want, where)
				}
			}
			if f.RunsInGo() {
				t.Errorf("a chain left something for Go, which cannot answer it:\n%s", f.Explain())
			}
		})
	}
}

// TestAChainWillNotRunInGo is the safety property the whole design turns on.
//
// In Go the far row is a map of columns, so the second hop is a missing key,
// which CEL reports as an error and Keep swallows as "does not match" — for
// every row. An empty listing that looks exactly like a correct one is the one
// answer this must never give, so a listing that did not take the SQL is told
// so instead.
func TestAChainWillNotRunInGo(t *testing.T) {
	f, err := Compile(&store.PR{}, `actions.exists(a, a.project.priority == 1)`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}

	// Keep without SQL: the listing never put the clause in its query.
	_, err = f.Keep(&store.PR{ID: "owner/repo#1"})
	if err == nil {
		t.Fatal("Keep answered without the query having run the chain")
	}
	for _, want := range []string{"two relations", "does not push filters"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to say %q", err, want)
		}
	}

	// Having taken the SQL, there is nothing left for Go and every row stands.
	f.SQL()
	kept, err := f.Keep(&store.PR{ID: "owner/repo#1"})
	if err != nil {
		t.Fatalf("Keep returned error after the query took the clause: %v", err)
	}
	if !kept {
		t.Error("a row was dropped by a filter the query had already answered")
	}
}

// TestAChainHasADepthLimit: parent.parent.parent… is finite and unbounded, and
// each level is a nested subquery, so the cost is multiplicative. The person
// waiting for the answer is the person who wrote the expression.
func TestAChainHasADepthLimit(t *testing.T) {
	expr := `children.exists(a, a.children.exists(b, b.children.exists(c, c.children.exists(d, d.title == "x"))))`
	_, err := Compile(&store.Project{}, expr)
	if err == nil {
		t.Fatal("a chain past the limit compiled")
	}
	if !strings.Contains(err.Error(), "relations deep") {
		t.Errorf("error = %v, want it to say how deep it went", err)
	}
}
