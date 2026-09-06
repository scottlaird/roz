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
			where, _ := f.Take()
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
	if where, _ := f.Take(); where != "" {
		t.Errorf("a bare outer reference was pushed down as %q", where)
	}

	// Qualified, it is one query.
	qualified, err := Compile(&store.Project{}, `children.exists(c, c.priority < project.priority)`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	if where, _ := qualified.Take(); where == "" {
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
	if where, _ := f.Take(); where != "" {
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
// TestWhatAChainStillWillNotDo. Three hops is the limit, and the reason is
// arithmetic rather than taste: each level is a nested subquery, so the cost
// multiplies, and the person waiting for the answer is the person who wrote
// the expression.
func TestWhatAChainStillWillNotDo(t *testing.T) {
	tests := []struct {
		name  string
		blank any
		expr  string
		want  string
	}{
		{
			name:  "past the depth limit",
			blank: &store.Project{},
			expr:  `children.exists(a, a.children.exists(b, b.children.exists(c, c.children.exists(d, d.title == "x"))))`,
			want:  "relations deep",
		},
		{
			// Two relations inside one term, which needs two subqueries and a
			// decision about how they compose. Answerable — it runs in Go —
			// but not pushed down, which is a demotion rather than a refusal.
			name:  "two relations in one term",
			blank: &store.Project{},
			expr:  `parent.title == "x" || parent.priority == 1`,
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(tc.blank, tc.expr)
			if tc.want == "" {
				// Answerable, whether or not every term reached the query.
				if err != nil {
					t.Fatalf("Compile(%q) returned error: %v", tc.expr, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Compile(%q) answered rather than refusing", tc.expr)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not say %q: %v", tc.want, err)
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
	if where, _ := demoted.Take(); where != "" {
		t.Error("the demoted term was pushed down after all")
	}

	// Not answerable: refused. Four hops, which is past the limit.
	deep := `children.exists(a, a.children.exists(b, b.children.exists(c, c.children.exists(d, d.title == "x"))))`
	if _, err := Compile(&store.Project{}, deep); err == nil {
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

	where, args := f.Take()
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
	if where, _ := f.Take(); where == "" {
		t.Error("a set membership test did not push down")
	}

	// A set of the wrong type does not: SQLite compares across types by
	// affinity where CEL calls it an error.
	mixed, err := Compile(&store.PR{}, `state in ["OPEN", 3]`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	if where, _ := mixed.Take(); where != "" {
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
			where, _ := f.Take()
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

// TestAChainInGoNeedsSomewhereToRead. The Go pass can follow a chain now — a
// far row loads the relations the expression names — but only if it was given
// a loader. Without one the second hop is a missing key, which CEL reports as
// an error and Keep would otherwise read as "does not match", for every row.
func TestAChainInGoNeedsSomewhereToRead(t *testing.T) {
	f, err := Compile(&store.PR{}, `actions.exists(a, a.project.priority == 1)`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}

	_, err = f.Keep(&store.PR{ID: "owner/repo#1"})
	if err == nil {
		t.Fatal("Keep answered with nowhere to read the far side from")
	}
	for _, want := range []string{"two relations", "nowhere to read"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to say %q", err, want)
		}
	}

	// Having taken the SQL, there is nothing left for Go and every row stands.
	f.Take()
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

// TestABaseAliasNamesTheOuterRowAsTheQueryDoes is what let `action list` push
// a traversal down at all: it selects `FROM action a`, so a subquery
// correlating to `action.id` names a table the query never mentions.
func TestABaseAliasNamesTheOuterRowAsTheQueryDoes(t *testing.T) {
	const expr = `subject_pr.state == "MERGED"`

	plain, err := Compile(&store.Action{}, expr)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	where, _ := plain.Take()
	if !strings.Contains(where, "= action.id") {
		t.Errorf("the default correlation is not by table name:\n%s", where)
	}

	aliased, err := Compile(&store.Action{}, expr, WithBaseAlias("a"))
	if err != nil {
		t.Fatalf("Compile with an alias returned error: %v", err)
	}
	where, _ = aliased.Take()
	if !strings.Contains(where, "= a.id") {
		t.Errorf("the alias did not reach the correlation:\n%s", where)
	}
	if strings.Contains(where, "action.id") {
		t.Errorf("the table name survived beside the alias:\n%s", where)
	}
}

// TestAnAliasedFilterQualifiesItsColumns. `action list --sort priority` joins
// project, and both tables have snooze_until: a bare column name is ambiguous
// and SQLite refuses the query rather than guessing. Found by the seeded
// expired_actions view, which is exactly that shape.
func TestAnAliasedFilterQualifiesItsColumns(t *testing.T) {
	f, err := Compile(&store.Action{},
		`state == "snoozed" && snooze_until != null`, WithBaseAlias("a"))
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	where, _ := f.Take()
	if where == "" {
		t.Skip("nothing was pushed down, so there is no qualification to check")
	}
	if strings.Contains(where, "(state") || strings.Contains(where, " state ") {
		t.Errorf("a bare column survived into a query that joins another table:\n%s", where)
	}
	if !strings.Contains(where, "a.state") {
		t.Errorf("the column was not qualified with the listing's alias:\n%s", where)
	}
}

// TestEachLevelGetsItsOwnAlias is the first of the two remaining pieces of
// #200, and the reason it was refused rather than answered: two levels of the
// same relation aliased alike would render `parent.id = parent.parent_id`,
// which SQLite runs and which compares a row to itself.
func TestEachLevelGetsItsOwnAlias(t *testing.T) {
	f, err := Compile(&store.Project{}, `parent.parent.title == "x"`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	where, args := f.Take()

	for _, want := range []string{
		"FROM project parent WHERE parent.id = project.parent_id",
		"FROM project parent_2 WHERE parent_2.id = parent.parent_id",
		"parent_2.title = ?",
	} {
		if !strings.Contains(where, want) {
			t.Errorf("SQL is missing %q:\n%s", want, where)
		}
	}
	if strings.Contains(where, "parent.id = parent.parent_id") {
		t.Errorf("a level was correlated to itself:\n%s", where)
	}
	if len(args) != 1 || args[0] != "x" {
		t.Errorf("args = %v, want the one literal", args)
	}

	// One hop keeps the readable alias, which is most of why the suffix is
	// only used where the name is taken.
	one, err := Compile(&store.Project{}, `parent.title == "x"`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	if where, _ := one.Take(); !strings.Contains(where, "FROM project parent ") {
		t.Errorf("a single hop stopped using the relation's own name:\n%s", where)
	}
}

// TestATraversalCanStartBehindAToOne is the second: `subject_pr.issues` is a
// hop before the quantifier rather than inside it, so the range of the
// traversal is what names the first relation.
//
// It is also the case where both levels join through a junction — action_pr
// then pr_tracker_issue — so the inner one shadows the outer's alias. Safe
// because nothing inside refers to the outer junction, and worth a test
// because it would not be safe if anything did.
func TestATraversalCanStartBehindAToOne(t *testing.T) {
	f, err := Compile(&store.Action{}, `subject_pr.issues.exists(i, i.status == "CLOSED")`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	where, args := f.Take()

	for _, want := range []string{
		"FROM pr subject_pr JOIN action_pr j",
		"j.action_id = action.id",
		"FROM tracker_issue i JOIN pr_tracker_issue j",
		"j.pr_id = subject_pr.id",
		"i.status = ?",
	} {
		if !strings.Contains(where, want) {
			t.Errorf("SQL is missing %q:\n%s", want, where)
		}
	}
	// The junction's own condition binds before the predicate's, which is the
	// order the statement writes them in.
	if len(args) != 2 || args[0] != store.RoleSubject || args[1] != "CLOSED" {
		t.Errorf("args = %v, want the role then the status", args)
	}
}

// TestGoFollowsASecondRelation is the last piece of #200: a far row loads the
// relations the expression names, so a chain evaluates here rather than being
// refused.
//
// It was refused because a far row was a map of columns, where `a.project` is
// a missing key — CEL calls that an error and Keep reads an error as "does not
// match", so every row silently failed. This is the same expression answering.
func TestGoFollowsASecondRelation(t *testing.T) {
	f, err := Compile(&store.PR{}, `actions.exists(a, a.project.priority == 1)`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}

	loader := &countingLoader{rows: map[string][]map[string]any{
		"action":  {{"id": "NA1", "verb": "merge"}},
		"project": {{"id": "SL1", "priority": int64(1)}},
	}}
	keep, err := f.WithLoader(context.Background(), loader).Keep(&store.PR{ID: "owner/repo#1"})
	if err != nil {
		t.Fatalf("Keep returned error: %v", err)
	}
	if !keep {
		t.Error("a row whose action advances a priority-1 project did not match")
	}
	// Both levels were read, and only those: the far row followed the one
	// relation the expression names.
	if len(loader.asked) != 2 || loader.asked[0] != "action" || loader.asked[1] != "project" {
		t.Errorf("read %v, want action then project", loader.asked)
	}

	// And the opposite answer, from the same shape.
	f, err = Compile(&store.PR{}, `actions.exists(a, a.project.priority == 9)`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	loader.asked = nil
	keep, err = f.WithLoader(context.Background(), loader).Keep(&store.PR{ID: "owner/repo#1"})
	if err != nil {
		t.Fatalf("Keep returned error: %v", err)
	}
	if keep {
		t.Error("a row matched a priority its project does not have")
	}
}

// TestGoReadsOnlyTheRelationsNamed. The second hop is a query per far row, so
// following one the expression never mentions is the difference between a
// filter and a table scan of everything.
func TestGoReadsOnlyTheRelationsNamed(t *testing.T) {
	f, err := Compile(&store.PR{}, `actions.exists(a, a.verb == "merge")`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	loader := &countingLoader{rows: map[string][]map[string]any{
		"action": {{"id": "NA1", "verb": "merge"}},
	}}
	if _, err := f.WithLoader(context.Background(), loader).Keep(&store.PR{ID: "owner/repo#1"}); err != nil {
		t.Fatalf("Keep returned error: %v", err)
	}
	if len(loader.asked) != 1 || loader.asked[0] != "action" {
		t.Errorf("read %v, want the one relation the filter names", loader.asked)
	}
}
