package store

import (
	"context"
	"testing"
	"time"
)

// TestIssuesToPollDecaysWithAge is the ladder, one case per rung and one on
// each side of it. The rule is the same everywhere: how long ago it closed
// decides how often it is worth asking, and synced_at is when it was last
// asked.
func TestIssuesToPollDecaysWithAge(t *testing.T) {
	st := newStore(t)
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		closedAt time.Duration // ago
		syncedAt time.Duration // ago
		want     bool
	}{
		{name: "closed minutes ago, read just now", closedAt: 10 * time.Minute, syncedAt: time.Second, want: true},
		{name: "closed yesterday, read an hour ago", closedAt: 20 * time.Hour, syncedAt: time.Hour, want: true},
		{name: "closed yesterday, read a minute ago", closedAt: 20 * time.Hour, syncedAt: time.Minute, want: false},
		{name: "closed last week, read two hours ago", closedAt: 5 * 24 * time.Hour, syncedAt: 2 * time.Hour, want: true},
		{name: "closed last week, read ten minutes ago", closedAt: 5 * 24 * time.Hour, syncedAt: 10 * time.Minute, want: false},
		{name: "closed last month, read yesterday", closedAt: 20 * 24 * time.Hour, syncedAt: 24 * time.Hour, want: true},
		{name: "closed last month, read an hour ago", closedAt: 20 * 24 * time.Hour, syncedAt: time.Hour, want: false},
		{name: "closed last year, read two days ago", closedAt: 200 * 24 * time.Hour, syncedAt: 48 * time.Hour, want: true},
		{name: "closed last year, read this morning", closedAt: 200 * 24 * time.Hour, syncedAt: 3 * time.Hour, want: false},
		{name: "closed years ago, read a fortnight ago", closedAt: 900 * 24 * time.Hour, syncedAt: 14 * 24 * time.Hour, want: true},
		{name: "closed years ago, read yesterday", closedAt: 900 * 24 * time.Hour, syncedAt: 24 * time.Hour, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "owner/repo#" + tt.name
			seedIssue(t, st, key,
				now.Add(-tt.closedAt).Format(timeFormat),
				now.Add(-tt.syncedAt).Format(timeFormat))

			polled := pollSet(t, st, now)
			if got := polled[key]; got != tt.want {
				t.Errorf("polled = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIssuesToPollAlwaysAsksAboutWhatIsOpen. Stored state, never what the
// tracker is about to say: the transition into closed is itself an
// observation, so an issue that is locally open has to stay in the set or it
// closes and is never seen to have closed.
func TestIssuesToPollAlwaysAsksAboutWhatIsOpen(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	// Open, and read a second ago: still asked about.
	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	issue := NewTrackerIssue(TrackerGitHub, "owner/repo#open")
	issue.SyncedAt = text(now.Add(-time.Second).Format(timeFormat))
	if err := tx.Insert(ctx, issue); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	if !pollSet(t, st, now)["owner/repo#open"] {
		t.Error("an open issue was left out of the poll set")
	}
}

// TestIssuesToPollAsksAboutWhatItHasNeverRead. A linked issue starts as a key
// and nothing else — which is most of what a pull request's closing references
// add — and the first read is what gives it a state to schedule against.
func TestIssuesToPollAsksAboutWhatItHasNeverRead(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.Insert(ctx, NewTrackerIssue(TrackerGitHub, "owner/repo#new")); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	if !pollSet(t, st, now)["owner/repo#new"] {
		t.Error("an issue nothing has ever read was left out of the poll set")
	}
}

// TestIssuesToPollIsScopedToItsTracker: a GitHub poll must not ask about a
// Jira key, which nothing reads and which would come back as a miss for ever.
func TestIssuesToPollIsScopedToItsTracker(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	if err := tx.Insert(ctx, NewTrackerIssue(TrackerJira, "CDSS-1744")); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	if pollSet(t, st, now)["CDSS-1744"] {
		t.Error("a Jira key is in the GitHub poll set")
	}
}

// TestIssuesToPollIsNotEveryIssue is the whole point, stated as a comparison:
// what is recorded and what is worth asking about are different questions, and
// they were the same query until closing references made the first one grow.
func TestIssuesToPollIsNotEveryIssue(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	for i, ago := range []time.Duration{time.Minute, 200 * 24 * time.Hour, 900 * 24 * time.Hour} {
		seedIssue(t, st, "owner/repo#"+string(rune('a'+i)),
			now.Add(-ago).Format(timeFormat),
			now.Add(-time.Minute).Format(timeFormat))
	}

	every, err := st.IssueKeys(ctx, TrackerGitHub)
	if err != nil {
		t.Fatalf("IssueKeys() returned error: %v", err)
	}
	if len(every) != 3 {
		t.Fatalf("IssueKeys() returned %d, want all 3", len(every))
	}
	if got := len(pollSet(t, st, now)); got != 1 {
		t.Errorf("polled %d of 3, want only the one closed a minute ago", got)
	}
}

// seedIssue records an issue as observed and closed, the way a sync would
// leave it.
func seedIssue(t *testing.T, st *Store, key, closedAt, syncedAt string) {
	t.Helper()
	ctx := context.Background()

	// As a sync, because closed_at, status and synced_at are observed: this
	// is seeding the state a poll would have left, not something a person
	// could type.
	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	issue := NewTrackerIssue(TrackerGitHub, key)
	issue.Status = text("CLOSED")
	issue.ClosedAt = text(closedAt)
	issue.SyncedAt = text(syncedAt)
	if err := tx.Insert(ctx, issue); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
}

func pollSet(t *testing.T, st *Store, now time.Time) map[string]bool {
	t.Helper()

	schedule := DefaultIssueSchedule
	schedule.Now = now.Format(timeFormat)
	keys, err := st.IssuesToPoll(context.Background(), TrackerGitHub, schedule)
	if err != nil {
		t.Fatalf("IssuesToPoll() returned error: %v", err)
	}
	set := map[string]bool{}
	for _, key := range keys {
		set[key] = true
	}
	return set
}
