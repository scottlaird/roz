package store

import (
	"context"
	"testing"
	"time"
)

// TestSnoozeExpiredAgreesWithTheQuery holds the Go verdict to the SQL one at
// the instant they could disagree. A snooze is stored as a bare date and the
// comparison is textual, so "until the 24th" is past from the first instant
// of the 24th; the page once compared against the date alone and showed an
// item the query said had expired with nothing marking it.
func TestSnoozeExpiredAgreesWithTheQuery(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	a := addAction(t, st, "deferred", "decide")
	snoozeUntil(t, st, a, "2026-08-24")
	p := addProject(t, st, "deferred project")
	snoozeProject(t, st, p, "2026-08-24")

	for _, tc := range []struct {
		at   string
		want bool
	}{
		{"2026-08-23T23:59:59.999Z", false},
		{"2026-08-24T00:00:00.000Z", true},
		{"2026-08-25T12:00:00.000Z", true},
	} {
		st.now = func() time.Time { return at(t, tc.at) }
		now := st.now().UTC().Format(timeFormat)

		listed := len(listIDs(t, st, ActionFilter{Expired: true})) == 1
		if got := loadAction(t, st, a.ID).SnoozeExpired(now); got != tc.want || listed != tc.want {
			t.Errorf("at %s: action SnoozeExpired = %v, --expired lists it = %v, want both %v",
				tc.at, got, listed, tc.want)
		}

		projects, err := st.ListProjects(ctx, ProjectFilter{Expired: true})
		if err != nil {
			t.Fatalf("ListProjects() returned error: %v", err)
		}
		if got := reloadProject(t, st, p.ID).SnoozeExpired(now); got != tc.want || (len(projects) == 1) != tc.want {
			t.Errorf("at %s: project SnoozeExpired = %v, --expired lists it = %v, want both %v",
				tc.at, got, len(projects) == 1, tc.want)
		}
	}
}
