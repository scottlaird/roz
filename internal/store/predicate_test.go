package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
func nullBool(b bool) sql.NullBool    { return sql.NullBool{Bool: b, Valid: true} }
func nullInt(n int64) sql.NullInt64   { return sql.NullInt64{Int64: n, Valid: true} }

// TestPredicates covers each predicate at its boundary, and — the case that
// matters most — that an unsynced pull request is never reported done.
func TestPredicates(t *testing.T) {
	tests := []struct {
		name string
		key  string
		pr   PR
		want bool
	}{
		// pr_not_draft
		{name: "not draft", key: PredicateNotDraft, pr: PR{IsDraft: nullBool(false)}, want: true},
		{name: "still draft", key: PredicateNotDraft, pr: PR{IsDraft: nullBool(true)}},
		{name: "draftness unknown", key: PredicateNotDraft, pr: PR{}},

		// pr_announced
		{name: "announced", key: PredicateAnnounced, pr: PR{AnnouncedAt: nullStr("2026-08-09")}, want: true},
		{name: "not announced", key: PredicateAnnounced, pr: PR{}},
		{name: "announced at nothing", key: PredicateAnnounced, pr: PR{AnnouncedAt: nullStr("")}},

		// pr_approved
		{name: "approved", key: PredicateApproved, pr: PR{ReviewDecision: nullStr("APPROVED")}, want: true},
		{name: "changes requested", key: PredicateApproved, pr: PR{ReviewDecision: nullStr("CHANGES_REQUESTED")}},
		{name: "review required", key: PredicateApproved, pr: PR{ReviewDecision: nullStr("REVIEW_REQUIRED")}},
		{name: "no review decision", key: PredicateApproved, pr: PR{}},

		// pr_threads_clear
		{name: "no unresolved threads", key: PredicateThreadsClear, pr: PR{UnresolvedThreads: nullInt(0)}, want: true},
		{name: "one unresolved thread", key: PredicateThreadsClear, pr: PR{UnresolvedThreads: nullInt(1)}},
		// NULL is never synced, which is not the same as none.
		{name: "threads never counted", key: PredicateThreadsClear, pr: PR{}},

		// pr_mergeable
		{name: "clean", key: PredicateMergeable, pr: PR{MergeStateStatus: nullStr("CLEAN")}, want: true},
		{name: "blocked is not a rebase problem", key: PredicateMergeable, pr: PR{MergeStateStatus: nullStr("BLOCKED")}, want: true},
		{name: "unstable is not a rebase problem", key: PredicateMergeable, pr: PR{MergeStateStatus: nullStr("UNSTABLE")}, want: true},
		{name: "behind", key: PredicateMergeable, pr: PR{MergeStateStatus: nullStr(MergeStateBehind)}},
		{name: "dirty", key: PredicateMergeable, pr: PR{MergeStateStatus: nullStr(MergeStateDirty)}},
		{name: "merge state unknown", key: PredicateMergeable, pr: PR{}},
		{name: "merge state empty", key: PredicateMergeable, pr: PR{MergeStateStatus: nullStr("")}},

		// pr_merged
		{name: "merged", key: PredicateMerged, pr: PR{State: nullStr(PRStateMerged)}, want: true},
		{name: "open", key: PredicateMerged, pr: PR{State: nullStr(PRStateOpen)}},
		{name: "closed unmerged", key: PredicateMerged, pr: PR{State: nullStr(PRStateClosed)}},
		{name: "state unknown", key: PredicateMerged, pr: PR{}},
	}

	for _, tt := range tests {
		t.Run(tt.key+"/"+tt.name, func(t *testing.T) {
			predicate, ok := LookupPredicate(tt.key)
			if !ok {
				t.Fatalf("no predicate registered for %q", tt.key)
			}
			if got := predicate(Facts{PR: &tt.pr}); got != tt.want {
				t.Errorf("%s(%+v) = %v, want %v", tt.key, tt.pr, got, tt.want)
			}
		})
	}
}

// TestNothingIsDoneBeforeSync states the rule the individual cases follow: a
// pull request nobody has looked at closes nothing.
//
// Facts carries only the pull request, so this also covers the predicates that
// read something else: with no ref wait and no refs, ref_exists must be false
// for the same reason.
func TestNothingIsDoneBeforeSync(t *testing.T) {
	fresh := NewPR("owner/repo", 1)

	for _, key := range PredicateKeys() {
		predicate, _ := LookupPredicate(key)
		if predicate(Facts{PR: fresh}) {
			t.Errorf("%s reported done for a pull request that has never been synced", key)
		}
	}
}

// TestEveryVerbPredicateIsRegistered is the check that would otherwise fail
// at three in the morning: the seeded vocabulary and the code must agree.
func TestEveryVerbPredicateIsRegistered(t *testing.T) {
	st := newStore(t)

	verbs, err := st.ListVerbs(context.Background(), true, Sort{}, SQLWhere{})
	if err != nil {
		t.Fatalf("ListVerbs() returned error: %v", err)
	}
	if len(verbs) == 0 {
		t.Fatal("the vocabulary is empty; the seed migration did not run")
	}

	var predicateVerbs int
	for _, v := range verbs {
		if v.Closes != ClosesPredicate {
			continue
		}
		predicateVerbs++
		if _, ok := v.Predicate(); !ok {
			t.Errorf("verb %q names predicate %q, which is not registered", v.Verb, v.PredicateKey.String)
		}
	}
	if predicateVerbs == 0 {
		t.Error("no verb closes on a predicate; the vocabulary looks wrong")
	}
}

// TestVocabularyMatchesTheSchemasRule checks the CHECK that ties the two
// columns together is respected by the seed: a predicate key exists exactly
// when the verb closes on one.
func TestVocabularyMatchesTheSchemasRule(t *testing.T) {
	st := newStore(t)

	verbs, err := st.ListVerbs(context.Background(), false, Sort{}, SQLWhere{})
	if err != nil {
		t.Fatalf("ListVerbs() returned error: %v", err)
	}
	for _, v := range verbs {
		hasKey := v.PredicateKey.Valid
		closesOnOne := v.Closes == ClosesPredicate
		if hasKey != closesOnOne {
			t.Errorf("verb %q closes %q but predicate_key valid = %v", v.Verb, v.Closes, hasKey)
		}
	}
}

// TestOpenStoreRefusesAnUnknownPredicate is the loud failure the sketch asks
// for. A verb whose function this build lacks stops the tool at startup.
func TestOpenStoreRefusesAnUnknownPredicate(t *testing.T) {
	path := newDBPath(t)
	if _, err := Init(path, testPrefixes()); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}

	// A verb from a newer build, whose function does not exist here.
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	_, err = db.Exec(`INSERT INTO actionverb (verb, label, closes, predicate_key, rank_class)
	                  VALUES ('teleport', 'teleport', 'predicate', 'pr_teleported', 'click')`)
	if err != nil {
		t.Fatalf("seeding the unknown verb: %v", err)
	}
	db.Close()

	_, err = OpenStore(path)
	if err == nil {
		t.Fatal("OpenStore() accepted a verb with no registered predicate, want an error")
	}
	var unregistered *ErrUnregisteredPredicate
	if !errors.As(err, &unregistered) {
		t.Fatalf("error = %v, want ErrUnregisteredPredicate", err)
	}
	if unregistered.Verb != "teleport" || unregistered.Key != "pr_teleported" {
		t.Errorf("error names %q/%q, want teleport/pr_teleported", unregistered.Verb, unregistered.Key)
	}
	// And it says what it does know, so the fix is obvious.
	if !strings.Contains(err.Error(), PredicateMerged) {
		t.Errorf("error = %v, want it to list the known predicates", err)
	}
}

// TestRetiredVerbsDoNotBlockStartup: a verb withdrawn along with its function
// should not stop the tool. Rows are never deleted, only deactivated.
func TestRetiredVerbsDoNotBlockStartup(t *testing.T) {
	path := newDBPath(t)
	if _, err := Init(path, testPrefixes()); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	_, err = db.Exec(`INSERT INTO actionverb (verb, label, closes, predicate_key, rank_class, active)
	                  VALUES ('teleport', 'teleport', 'predicate', 'pr_teleported', 'click', 0)`)
	if err != nil {
		t.Fatalf("seeding the retired verb: %v", err)
	}
	db.Close()

	st, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() refused a retired verb: %v", err)
	}
	st.Close()
}
