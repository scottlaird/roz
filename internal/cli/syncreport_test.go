package cli

import (
	"bytes"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/scottlaird/roz/internal/ghsync"
	"github.com/scottlaird/roz/internal/github"
	"github.com/scottlaird/roz/internal/store"
	"github.com/spf13/cobra"
)

// unreportedResultFields are the exported fields of ghsync.Result that the
// sync report deliberately says nothing about, each with the reason. A field
// missing from both the report and this list fails the test below.
var unreportedResultFields = map[string]string{
	"RateLimit": "a pacing signal for the syncer, exported on /metrics; a number that changes every cycle is noise in a report of what changed",
}

// reportedSample is a Result with every exported field populated with one
// realistic element, so that zeroing any one of them removes something from
// the report. Adding a field to Result means adding it here, which is the
// moment to add its report line too.
func reportedSample() ghsync.Result {
	action := func(id string) *store.Action {
		return &store.Action{ID: id, Title: "title of " + id, Verb: "write", State: store.ActionReady}
	}
	return ghsync.Result{
		Polled:     1,
		Changed:    map[string][]store.Change{"owner/repo#1": {{}}},
		Missing:    map[string]string{"owner/repo#2": "gone"},
		Backfilled: map[string]int{"owner/repo tag": 3},

		IssuesPolled:         1,
		Issues:               []store.TrackerApplied{{Key: "PROJ-1", Changes: []store.Change{{}}}},
		IssuesClosedWithWork: []string{"PROJ-2 closed with NA9 still open"},

		Owners:     []ghsync.Owners{{PR: "owner/repo#3", Required: []string{"@team"}}},
		Resolved:   []store.Resolved{{ActionID: "NA3", Spec: "v1.2.x"}},
		Truncated:  []github.RefTruncation{{Query: github.RefQuery{Repo: "owner/repo"}, Matched: 500, Read: 100}},
		NewRefs:    []*store.GitRef{{RepoID: "owner/repo", Kind: store.RefTag, Name: "v1.2.3"}},
		RefsPolled: 1,

		Settled:     []store.Settled{{Action: action("NA4"), PR: "owner/repo#4", Result: &store.CloseResult{}}},
		Ejected:     []store.Ejected{{PR: "owner/repo#5", Action: action("NA5")}},
		Overdue:     []store.Overdue{{Action: action("NA6"), Raised: action("NA7"), Waiting: 3}},
		Unannounced: []store.Unannounced{{Action: action("NA8"), PR: "owner/repo#8"}},
		Woken:       store.Woken{Actions: []*store.Action{action("NA9")}, Projects: []*store.Project{{ID: "SL9"}}},
		Stacked:     []*store.PR{{ID: "owner/repo#10", StackedOn: sql.NullString{String: "owner/repo#9", Valid: true}}},
		Failed:      []ghsync.ReadFailure{{Read: "refs", Err: errors.New("rate limited")}},

		Teams:     1,
		RateLimit: github.RateLimit{Remaining: 4000, Limit: 5000},
	}
}

func renderReport(t *testing.T, result ghsync.Result) string {
	t.Helper()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := reportSync(cmd, result, false); err != nil {
		t.Fatalf("reportSync() returned error: %v", err)
	}
	return out.String()
}

// TestEveryResultFieldIsReported is the check the audit found missing: Result
// had grown to twenty-odd fields, the report in another package printed each
// by hand, and two of them -- Stacked and Failed -- had never been printed at
// all. Now a field is either in the report, in the exception list with a
// reason, or a failing test.
func TestEveryResultFieldIsReported(t *testing.T) {
	full := reportedSample()
	whole := renderReport(t, full)

	typ := reflect.TypeOf(full)
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		if _, skip := unreportedResultFields[f.Name]; skip {
			continue
		}
		value := reflect.ValueOf(full).Field(i)
		if value.IsZero() {
			t.Errorf("Result.%s is not set in reportedSample, so nothing checks that it is reported", f.Name)
			continue
		}
		// The same result with this one field empty: if the report does not
		// change, the field is never printed.
		without := full
		reflect.ValueOf(&without).Elem().Field(i).Set(reflect.Zero(f.Type))
		if renderReport(t, without) == whole {
			t.Errorf("Result.%s is populated but the sync report does not mention it", f.Name)
		}
	}
	for name := range unreportedResultFields {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("unreportedResultFields names %s, which Result no longer has", name)
		}
	}
}
