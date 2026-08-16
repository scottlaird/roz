package cli

import (
	"sort"
	"strings"
	"testing"
)

// TestFlagsAndFiltersAgree is what makes the explanations worth printing.
//
// `--orphaned is --filter '…'` is a claim, and a claim in a string rots the
// moment either side moves — the flag's WHERE clause and the expression are
// two spellings of one rule with nothing holding them together. This runs both
// against the same rows and compares what comes back, which is the bargain
// schema.sql already makes with the migrations: content in a file is only
// worth having if something checks it.
//
// Not overkill, and the reason is that these two are *meant* to drift apart.
// The flags stay hand-written because a tested one-line clause beats a parse
// and a conversion; so nothing else would ever notice if `--open` grew a
// condition and its explanation did not.
//
// Only the ones claimed exact. `--unblocked` has no expression, and
// `--expired` needs a clock the vocabulary does not have; those carry a note
// instead, and a note is not a claim about rows.
func TestFlagsAndFiltersAgree(t *testing.T) {
	db := fixtureForFlags(t)

	tests := []struct {
		listing string
		flag    []string
		known   map[string]flagMeaning
		name    string
	}{
		{listing: "pr", flag: []string{"--state", "MERGED"}, known: prFlagFilters, name: "state"},
		{listing: "action", flag: []string{"--open"}, known: actionFlagFilters, name: "open"},
		{listing: "action", flag: []string{"--verb", "merge"}, known: actionFlagFilters, name: "verb"},
		{listing: "action", flag: []string{"--status", "ready"}, known: actionFlagFilters, name: "status"},
		{listing: "project", flag: []string{"--status", "active"}, known: projectFlagFilters, name: "status"},
		{listing: "project", flag: []string{"--orphaned"}, known: projectFlagFilters, name: "orphaned"},
		{listing: "project", flag: []string{"--expired"}, known: projectFlagFilters, name: "expired"},
		{listing: "action", flag: []string{"--expired"}, known: actionFlagFilters, name: "expired"},
	}

	for _, tc := range tests {
		t.Run(tc.listing+" --"+tc.name, func(t *testing.T) {
			meaning, ok := tc.known[tc.name]
			if !ok || !meaning.exact {
				t.Fatalf("--%s does not claim to be exact", tc.name)
			}
			expression := meaning.filter
			if meaning.withValue != nil {
				expression = meaning.withValue(tc.flag[len(tc.flag)-1])
			}

			byFlag := idsFrom(t, append([]string{tc.listing, "list", "--db", db}, tc.flag...))
			byFilter := idsFrom(t, []string{tc.listing, "list", "--db", db, "--filter", expression})

			if len(byFlag) == 0 && len(byFilter) == 0 {
				t.Fatalf("both selected nothing; the fixture does not exercise --%s", tc.name)
			}
			if strings.Join(byFlag, ",") != strings.Join(byFilter, ",") {
				t.Errorf("--%s selected %v\n--filter %s selected %v",
					tc.name, byFlag, expression, byFilter)
			}
		})
	}
}

// TestTheFixtureExercisesBothSides guards the test above from passing because
// everything matched: two identical lists prove nothing if the filter was
// never asked to exclude anything.
func TestTheFixtureExercisesBothSides(t *testing.T) {
	db := fixtureForFlags(t)

	for _, tc := range []struct{ listing, flag string }{
		{"project", "--orphaned"},
		{"action", "--open"},
	} {
		all := idsFrom(t, []string{tc.listing, "list", "--db", db})
		some := idsFrom(t, []string{tc.listing, "list", "--db", db, tc.flag})
		if len(some) == 0 || len(some) == len(all) {
			t.Errorf("%s %s selected %d of %d; the fixture should leave some out",
				tc.listing, tc.flag, len(some), len(all))
		}
	}
}

// idsFrom runs a listing and returns its ids, sorted so the comparison is
// about membership rather than about order — the flags and the filter take
// different paths through the ORDER BY.
func idsFrom(t *testing.T, args []string) []string {
	t.Helper()

	out, err := runCLI(t, append(args, "--fields", "id", "-o", "csv")...)
	if err != nil {
		t.Fatalf("%v returned error: %v", args, err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return nil
	}
	ids := lines[1:]
	sort.Strings(ids)
	return ids
}

// fixtureForFlags builds rows that each flag both keeps and drops.
func fixtureForFlags(t *testing.T) string {
	t.Helper()
	db, key := trackedPR(t)

	if _, err := runCLI(t, "pr", "track", "--db", db, "owner/repo#2"); err != nil {
		t.Fatalf("pr track returned error: %v", err)
	}
	observeStates(t, db, map[string]string{key: "MERGED", "owner/repo#2": "OPEN"})

	// A project with an open action, one with none, and one finished.
	//
	// The open one is a `write`, which closes when a person says so. A merge
	// step against the merged pull request would settle the moment it was
	// created, leaving nothing for --open to find.
	// Snoozed to a date that has passed, so --expired has something to find
	// and something to leave out.
	stale := addProject(t, db, "snoozed too long")
	if _, err := runCLI(t, "project", "snooze", "--db", db, stale,
		"--snooze-until", "2020-01-01"); err != nil {
		t.Fatalf("project snooze returned error: %v", err)
	}

	live := addProject(t, db, "live work")
	addAction(t, db, "--title", "write it", "--verb", "write", "--project", live)
	overdue := addAction(t, db, "--title", "later", "--verb", "write", "--project", live)
	if _, err := runCLI(t, "action", "snooze", "--db", db, overdue,
		"--snooze-until", "2020-01-01"); err != nil {
		t.Fatalf("action snooze returned error: %v", err)
	}
	addAction(t, db, "--title", "merge it", "--verb", "merge", "--project", live, "--pr", key)
	addProject(t, db, "nobody is working on this")
	done := addProject(t, db, "finished")
	if _, err := runCLI(t, "project", "close", "--db", db, done); err != nil {
		t.Fatalf("project close returned error: %v", err)
	}
	return db
}
