package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"

	"github.com/scottlaird/roz/internal/store"
)

// seedRefs records two tags, which is what a listing needs to have something
// to lay out. Written straight to the table because sync is the only thing
// that observes a ref, and this is about the output rather than the reading.
func seedRefs(t *testing.T, db string) {
	t.Helper()
	trackRepo(t, db, "cli/cli")
	observeRefs(t, db, "cli/cli", "v2.96.0", "v2.97.0")
}

// TestRefListDefaultView: the columns the hand-written table showed, in the
// order it showed them.
func TestRefListDefaultView(t *testing.T) {
	db := initDB(t)
	seedRefs(t, db)

	out, err := runCLI(t, "ref", "list", "--db", db)
	if err != nil {
		t.Fatalf("ref list returned error: %v", err)
	}
	header := strings.SplitN(out, "\n", 2)[0]
	for _, want := range []string{"REPOSITORY", "KIND", "NAME", "COMMIT", "FIRST SEEN"} {
		if !strings.Contains(header, want) {
			t.Errorf("header does not carry %q:\n%s", want, out)
		}
	}
	// The identifier is reachable by name and not in the default view: it is
	// the three columns beside it spelled as one string.
	if strings.Contains(header, "OBSERVED AT") {
		t.Errorf("the default view shows a column it should not:\n%s", out)
	}
	if !strings.Contains(out, "cli/cli") {
		t.Errorf("the repository is missing from the rows:\n%s", out)
	}
}

// TestFieldsChoosesColumns, including the info dump, which is the only way to
// reach a column the default view leaves out.
func TestFieldsChoosesColumns(t *testing.T) {
	db := initDB(t)
	seedRefs(t, db)

	out, err := runCLI(t, "ref", "list", "--db", db, "--fields", "name,commit_sha")
	if err != nil {
		t.Fatalf("ref list --fields returned error: %v", err)
	}
	header := strings.SplitN(out, "\n", 2)[0]
	if strings.Contains(header, "REPOSITORY") || strings.Contains(header, "KIND") {
		t.Errorf("--fields did not narrow the table:\n%s", out)
	}
	// In the order asked for, not the order the record declares them.
	if !strings.HasPrefix(header, "NAME") {
		t.Errorf("--fields did not order the columns as asked:\n%s", out)
	}

	all, err := runCLI(t, "ref", "list", "--db", db, "--fields", "all")
	if err != nil {
		t.Fatalf("ref list --fields all returned error: %v", err)
	}
	for _, want := range []string{"ID", "OBSERVED AT", "FIRST SEEN"} {
		if !strings.Contains(all, want) {
			t.Errorf("--fields all does not carry %q:\n%s", want, all)
		}
	}
}

// TestFieldsRefusesAColumnThatIsNotOne, naming the vocabulary rather than
// leaving the caller to guess at it.
func TestFieldsRefusesAColumnThatIsNotOne(t *testing.T) {
	db := initDB(t)
	seedRefs(t, db)

	_, err := runCLI(t, "ref", "list", "--db", db, "--fields", "name,nope")
	if err == nil {
		t.Fatal("ref list accepted a column that does not exist")
	}
	for _, want := range []string{"nope", "commit_sha", "all"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}

	// A trailing comma is a typo, not a request for a blank column.
	if _, err := runCLI(t, "ref", "list", "--db", db, "--fields", "name,"); err == nil {
		t.Error("ref list accepted a trailing comma")
	}
}

// TestCSVOutput: the header is the column names, and there is one even when
// there are no rows — something reading this should find the shape it
// expected rather than nothing at all.
func TestCSVOutput(t *testing.T) {
	db := initDB(t)
	seedRefs(t, db)

	out, err := runCLI(t, "ref", "list", "--db", db, "-o", "csv")
	if err != nil {
		t.Fatalf("ref list -o csv returned error: %v", err)
	}
	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("the output is not CSV: %v\n%s", err, out)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d CSV rows, want a header and two refs:\n%s", len(rows), out)
	}
	if rows[0][0] != "repo_id" {
		t.Errorf("the header is not the column names: %v", rows[0])
	}

	empty, err := runCLI(t, "ref", "list", "--db", db, "acme/nothing", "-o", "csv")
	if err != nil {
		t.Fatalf("ref list -o csv returned error: %v", err)
	}
	if !strings.HasPrefix(empty, "repo_id,") {
		t.Errorf("an empty result dropped the header:\n%s", empty)
	}
}

// TestNarrowingNeverAltersAValue is the rule the formats are split on. A
// table abbreviates a commit because a reader skims it; CSV and JSON carry
// the commit, because they are read by something that wants the value.
func TestNarrowingNeverAltersAValue(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "cli/cli")
	observeRefs(t, db, "cli/cli", "v2.96.0")

	const full = "abc1234def5678901234567890abcdef12345678"

	table, err := runCLI(t, "ref", "list", "--db", db, "--fields", "commit_sha")
	if err != nil {
		t.Fatalf("ref list returned error: %v", err)
	}
	if strings.Contains(table, full) {
		t.Errorf("the table did not abbreviate the commit:\n%s", table)
	}
	if !strings.Contains(table, full[:8]) {
		t.Errorf("the table lost the commit:\n%s", table)
	}

	for _, format := range []string{"csv", "json"} {
		out, err := runCLI(t, "ref", "list", "--db", db, "--fields", "commit_sha", "-o", format)
		if err != nil {
			t.Fatalf("ref list -o %s returned error: %v", format, err)
		}
		if !strings.Contains(out, full) {
			t.Errorf("-o %s carried the abbreviation rather than the commit:\n%s", format, out)
		}
	}
}

// TestJSONCarriesEverythingUntilAsked keeps the promise `-o json` already
// made: something parsing it should not lose a column because the table's
// default view does not show one.
func TestJSONCarriesEverythingUntilAsked(t *testing.T) {
	db := initDB(t)
	seedRefs(t, db)

	out, err := runCLI(t, "ref", "list", "--db", db, "-o", "json")
	if err != nil {
		t.Fatalf("ref list -o json returned error: %v", err)
	}
	var full []map[string]any
	if err := json.Unmarshal([]byte(out), &full); err != nil {
		t.Fatalf("the output is not JSON: %v\n%s", err, out)
	}
	// observed_at is in no default view, and is still here.
	for _, want := range []string{"id", "repo_id", "name", "kind", "commit_sha", "first_seen", "observed_at"} {
		if _, ok := full[0][want]; !ok {
			t.Errorf("-o json dropped %q: %v", want, full[0])
		}
	}

	narrowed, err := runCLI(t, "ref", "list", "--db", db, "-o", "json", "--fields", "name")
	if err != nil {
		t.Fatalf("ref list -o json --fields returned error: %v", err)
	}
	var some []map[string]any
	if err := json.Unmarshal([]byte(narrowed), &some); err != nil {
		t.Fatalf("the output is not JSON: %v\n%s", err, narrowed)
	}
	if len(some[0]) != 1 {
		t.Errorf("--fields did not narrow the JSON: %v", some[0])
	}
}

// TestShowDoesNotOfferCSV: a detail view is a column per line, so CSV of one
// would be two columns of nothing anybody wants. Refused rather than accepted
// and quietly printing a table.
func TestShowDoesNotOfferCSV(t *testing.T) {
	db := initDB(t)
	id := addAction(t, db, "--title", "Write it", "--verb", "write")

	_, err := runCLI(t, "action", "show", "--db", db, id, "-o", "csv")
	if err == nil {
		t.Fatal("action show accepted -o csv")
	}
	if !strings.Contains(err.Error(), "table or json") {
		t.Errorf("error does not name what show accepts: %v", err)
	}
}

// TestDerivedColumnIsTheValueItRenders: a column with nothing behind it in the
// record has only what it renders to, so that is what every format carries.
func TestDerivedColumnIsTheValueItRenders(t *testing.T) {
	type row struct {
		Name string `db:"name" kind:"identity"`
	}
	set := columnSet[*row]{
		blank: &row{},
		declared: []column[*row]{
			{name: "shout", render: func(r *row, _ renderContext) string {
				return strings.ToUpper(r.Name)
			}},
		},
		defaults: []string{"name", "shout"},
		empty:    "nothing",
	}

	var out bytes.Buffer
	if err := writeRecords(&out, outputJSON, set, []string{"shout"}, []*row{{Name: "quiet"}}, renderContext{}); err != nil {
		t.Fatalf("writeRecords() returned error: %v", err)
	}
	if !strings.Contains(out.String(), `"shout":"QUIET"`) {
		t.Errorf("the derived column is not in the JSON: %s", out.String())
	}

	out.Reset()
	if err := writeRecords(&out, outputTable, set, nil, []*row{{Name: "quiet"}}, renderContext{}); err != nil {
		t.Fatalf("writeRecords() returned error: %v", err)
	}
	if !strings.Contains(out.String(), "SHOUT") || !strings.Contains(out.String(), "QUIET") {
		t.Errorf("the derived column is not in the table: %s", out.String())
	}
}

// TestShowIfKeepsAColumnOutOfTheDefaultView, and asking for it by name is the
// answer to whether it is relevant.
func TestShowIfKeepsAColumnOutOfTheDefaultView(t *testing.T) {
	type row struct {
		Name string `db:"name" kind:"identity"`
		Note string `db:"note"`
	}
	set := columnSet[*row]{
		blank: &row{},
		declared: []column[*row]{
			{name: "note", showIf: func(rows []*row, _ renderContext) bool {
				for _, r := range rows {
					if r.Note != "" {
						return true
					}
				}
				return false
			}},
		},
		defaults: []string{"name", "note"},
		empty:    "nothing",
	}

	var out bytes.Buffer
	if err := writeRecords(&out, outputTable, set, nil, []*row{{Name: "a"}}, renderContext{}); err != nil {
		t.Fatalf("writeRecords() returned error: %v", err)
	}
	if strings.Contains(out.String(), "NOTE") {
		t.Errorf("an unused column is in the default view: %s", out.String())
	}

	out.Reset()
	if err := writeRecords(&out, outputTable, set, nil, []*row{{Name: "a", Note: "here"}}, renderContext{}); err != nil {
		t.Fatalf("writeRecords() returned error: %v", err)
	}
	if !strings.Contains(out.String(), "NOTE") {
		t.Errorf("a used column is missing from the default view: %s", out.String())
	}

	// Asked for by name, it appears whatever is in it.
	out.Reset()
	if err := writeRecords(&out, outputTable, set, []string{"note"}, []*row{{Name: "a"}}, renderContext{}); err != nil {
		t.Fatalf("writeRecords() returned error: %v", err)
	}
	if !strings.Contains(out.String(), "NOTE") {
		t.Errorf("a column asked for by name was hidden: %s", out.String())
	}
}

// TestEveryListingDeclaresItsColumns is the rollout's own check: each
// converted listing takes --fields and -o csv, and names a column it should
// have. A listing that regressed to a fixed table fails here rather than the
// next time somebody tries to select a column on it.
func TestEveryListingDeclaresItsColumns(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "cli/cli")

	for _, tc := range []struct {
		listing []string
		column  string
	}{
		{[]string{"action", "list"}, "late"},
		{[]string{"project", "list"}, "priority"},
		{[]string{"pr", "list"}, "merged_at"},
		{[]string{"issue", "list"}, "closed_at"},
		{[]string{"ref", "list"}, "commit_sha"},
		{[]string{"repo", "list"}, "short_name"},
		{[]string{"calendar", "list"}, "capacity"},
		{[]string{"verb", "list"}, "wait_days"},
		{[]string{"pipeline", "list"}, "steps"},
	} {
		t.Run(strings.Join(tc.listing, " "), func(t *testing.T) {
			args := append(append([]string{}, tc.listing...), "--db", db, "--fields", tc.column)
			if _, err := runCLI(t, args...); err != nil {
				t.Errorf("--fields %s returned error: %v", tc.column, err)
			}
			args = append(append([]string{}, tc.listing...), "--db", db, "-o", "csv")
			if _, err := runCLI(t, args...); err != nil {
				t.Errorf("-o csv returned error: %v", err)
			}
			// And the vocabulary is named when something is not in it.
			args = append(append([]string{}, tc.listing...), "--db", db, "--fields", "nope")
			if _, err := runCLI(t, args...); err == nil {
				t.Error("a column that does not exist was accepted")
			}
		})
	}
}

// TestARenderedNothingIsSpeltByTheFormat: a derived cell returns empty for
// "nothing here", and the format says how that reads. The table's dash is a
// mark for a person, and putting it in a CSV column would be data.
func TestARenderedNothingIsSpeltByTheFormat(t *testing.T) {
	db := initDB(t)
	addAction(t, db, "--title", "Write it", "--verb", "write")

	table, err := runCLI(t, "action", "list", "--db", db, "--fields", "id,late")
	if err != nil {
		t.Fatalf("action list returned error: %v", err)
	}
	if !strings.Contains(table, "-") {
		t.Errorf("the table does not mark an action that is not late:\n%s", table)
	}

	out, err := runCLI(t, "action", "list", "--db", db, "--fields", "id,late", "-o", "csv")
	if err != nil {
		t.Fatalf("action list -o csv returned error: %v", err)
	}
	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("the output is not CSV: %v\n%s", err, out)
	}
	if rows[1][1] != "" {
		t.Errorf("an action that is not late reads %q in CSV, want an empty field", rows[1][1])
	}
}

// TestTheTreeIndentsOnlyThePicture: --tree is what rows there are, which
// every format carries, and the indentation is how a table draws them.
func TestTheTreeIndentsOnlyThePicture(t *testing.T) {
	db := initDB(t)
	parent := addProject(t, db, "Split the nodepool")
	child := addProject(t, db, "Drain the old pool", "--parent", parent)

	table, err := runCLI(t, "project", "list", "--db", db, "--tree")
	if err != nil {
		t.Fatalf("project list --tree returned error: %v", err)
	}
	if !strings.Contains(table, "  Drain the old pool") {
		t.Errorf("the tree does not indent the child:\n%s", table)
	}

	out, err := runCLI(t, "project", "list", "--db", db, "--tree", "-o", "csv")
	if err != nil {
		t.Fatalf("project list --tree -o csv returned error: %v", err)
	}
	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("the output is not CSV: %v\n%s", err, out)
	}
	for _, row := range rows[1:] {
		if row[0] != child {
			continue
		}
		if title := row[len(row)-1]; title != "Drain the old pool" {
			t.Errorf("CSV carried the drawing rather than the title: %q", title)
		}
	}
}

// TestBooleansReadAsTheTableSpeltThem, and as a program expects everywhere
// else. `show` is left alone: converting the listings is not the moment to
// change what a different command prints.
func TestBooleansReadAsTheTableSpeltThem(t *testing.T) {
	db := initDB(t)

	table, err := runCLI(t, "verb", "list", "--db", db, "--fields", "verb,active")
	if err != nil {
		t.Fatalf("verb list returned error: %v", err)
	}
	if !strings.Contains(table, "yes") {
		t.Errorf("the table does not spell a boolean yes:\n%s", table)
	}

	out, err := runCLI(t, "verb", "list", "--db", db, "--fields", "verb,active", "-o", "csv")
	if err != nil {
		t.Fatalf("verb list -o csv returned error: %v", err)
	}
	if !strings.Contains(out, "true") {
		t.Errorf("CSV does not carry a boolean as one:\n%s", out)
	}
}

// equalStrings compares two lists, since a listing's whole answer is the
// order it came back in.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSortByColumns is what #169 asked for: several keys, primary first.
func TestSortByColumns(t *testing.T) {
	db := initDB(t)
	for _, title := range []string{"Zebra", "Apple", "Mango"} {
		addProject(t, db, title)
	}

	titles := func(args ...string) []string {
		t.Helper()
		out, err := runCLI(t, append([]string{
			"project", "list", "--db", db, "--fields", "title", "-o", "csv",
		}, args...)...)
		if err != nil {
			t.Fatalf("project list returned error: %v", err)
		}
		rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
		if err != nil {
			t.Fatalf("the output is not CSV: %v\n%s", err, out)
		}
		var got []string
		for _, row := range rows[1:] {
			got = append(got, row[0])
		}
		return got
	}

	// Creation order is what a listing has always come back in, and is what
	// --sort unset still means.
	if got := titles(); !equalStrings(got, []string{"Zebra", "Apple", "Mango"}) {
		t.Errorf("unsorted = %v, want creation order", got)
	}
	if got := titles("--sort", "title"); !equalStrings(got, []string{"Apple", "Mango", "Zebra"}) {
		t.Errorf("--sort title = %v", got)
	}
	if got := titles("--sort", "-title"); !equalStrings(got, []string{"Zebra", "Mango", "Apple"}) {
		t.Errorf("--sort -title = %v", got)
	}
	// Two keys: everything shares a status here, so the second decides.
	if got := titles("--sort", "status,title"); !equalStrings(got, []string{"Apple", "Mango", "Zebra"}) {
		t.Errorf("--sort status,title = %v", got)
	}
}

// TestSortKeepsTheRankings: priority and staleness are not columns — one is a
// CTE and two joins, the other a computed date — so they stay reserved words
// in the same flag rather than moving to one of their own.
func TestSortKeepsTheRankings(t *testing.T) {
	db := initDB(t)
	addProject(t, db, "Later", "--priority", "4")
	addProject(t, db, "Now", "--priority", "1")

	out, err := runCLI(t, "project", "list", "--db", db, "--sort", "priority", "--fields", "title")
	if err != nil {
		t.Fatalf("project list --sort priority returned error: %v", err)
	}
	if before, after := strings.Index(out, "Now"), strings.Index(out, "Later"); before > after {
		t.Errorf("--sort priority did not rank:\n%s", out)
	}

	// A ranking is the whole ordering, not the first key of one.
	_, err = runCLI(t, "project", "list", "--db", db, "--sort", "priority,title")
	if err == nil {
		t.Fatal("a ranking was accepted as a sort key")
	}
	if !strings.Contains(err.Error(), "takes no further keys") {
		t.Errorf("error does not explain why: %v", err)
	}
}

// TestSortRefusesWhatIsNotAColumn covers the derived columns, which have
// nothing behind them to sort on, and anything shaped like SQL.
func TestSortRefusesWhatIsNotAColumn(t *testing.T) {
	db := initDB(t)
	addAction(t, db, "--title", "Write it", "--verb", "write")

	for _, spec := range []string{
		"late",                     // derived: how late lives in a map, not a column
		"nope",                     //
		"title; DROP TABLE action", // the reason the names are checked at all
		"title DESC",               // direction is the - prefix, not SQL
		"title,",                   // a trailing comma is a typo
	} {
		if _, err := runCLI(t, "action", "list", "--db", db, "--sort", spec); err == nil {
			t.Errorf("action list accepted --sort %q", spec)
		}
	}

	// And the table is still there, which is the point of the whitelist.
	if _, err := runCLI(t, "action", "list", "--db", db); err != nil {
		t.Fatalf("action list returned error after the attempts: %v", err)
	}
}

// TestSortOrdersTheTreeWithoutFlatteningIt: the ordering decides roots and
// siblings, and the hierarchy still holds — Tree walks its input in order, so
// this falls out rather than being arranged.
func TestSortOrdersTheTreeWithoutFlatteningIt(t *testing.T) {
	db := initDB(t)
	parent := addProject(t, db, "Apple")
	addProject(t, db, "Mango", "--parent", parent)
	addProject(t, db, "Beta", "--parent", parent)
	addProject(t, db, "Zebra")

	out, err := runCLI(t, "project", "list", "--db", db, "--tree", "--sort", "title", "--fields", "title")
	if err != nil {
		t.Fatalf("project list --tree --sort returned error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")[1:]
	want := []string{"Apple", "  Beta", "  Mango", "Zebra"}
	for i, line := range lines {
		if i >= len(want) {
			break
		}
		if strings.TrimRight(line, " ") != want[i] {
			t.Errorf("line %d = %q, want %q\n%s", i, line, want[i], out)
		}
	}
}

// TestSortBeatsAListingsOwnDefault: --since orders by when something merged,
// and asking for an order says which one wins.
func TestSortBeatsAListingsOwnDefault(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api")
	for _, n := range []string{"1", "2"} {
		if _, err := runCLI(t, "pr", "track", "--db", db, "acme/api#"+n); err != nil {
			t.Fatalf("pr track returned error: %v", err)
		}
	}
	mergePRAt(t, db, "acme/api#1", "2026-08-05T09:00:00.000Z")
	mergePRAt(t, db, "acme/api#2", "2026-08-07T09:00:00.000Z")

	oldest, err := runCLI(t, "pr", "list", "--db", db, "--since", "2026-08-01", "--fields", "id")
	if err != nil {
		t.Fatalf("pr list --since returned error: %v", err)
	}
	if strings.Index(oldest, "#1") > strings.Index(oldest, "#2") {
		t.Errorf("--since is not oldest first:\n%s", oldest)
	}

	newest, err := runCLI(t, "pr", "list", "--db", db,
		"--since", "2026-08-01", "--sort", "-merged_at", "--fields", "id")
	if err != nil {
		t.Fatalf("pr list --sort returned error: %v", err)
	}
	if strings.Index(newest, "#2") > strings.Index(newest, "#1") {
		t.Errorf("--sort did not override the window's own order:\n%s", newest)
	}
}

// mergePRAt records a merge the way sync would.
func mergePRAt(t *testing.T, db, id, at string) {
	t.Helper()

	st, err := store.OpenStore(db)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	tx, err := st.Begin(ctx, store.ActorSyncGitHub)
	if err != nil {
		t.Fatalf("beginning a transaction: %v", err)
	}
	defer tx.Rollback()

	before, err := tx.LoadPR(ctx, id)
	if err != nil {
		t.Fatalf("loading %s: %v", id, err)
	}
	after := before.Clone()
	after.State = sql.NullString{String: store.PRStateMerged, Valid: true}
	after.MergedAt = sql.NullString{String: at, Valid: true}
	if _, err := tx.Update(ctx, before, after); err != nil {
		t.Fatalf("recording the merge: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("committing: %v", err)
	}
}
