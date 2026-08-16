package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// IssueSchedule is how often an issue is worth asking about, by how long ago
// it closed.
//
// Issues were polled flat: every recorded issue, every cycle, for ever. That
// was affordable while the only issues recorded were ones a person had linked
// by hand, and stopped being affordable when a pull request's closing
// references started adding one per reference (#167) — the set only grows, and
// nothing untracks a row.
//
// A decay rather than a cutoff, because a closed issue can come back and the
// chance of that falls off sharply rather than to zero. An issue closed in the
// last hour is quite likely to reopen; one closed two years ago is not, but is
// not impossible either, so nothing is ever dropped.
type IssueSchedule struct {
	// Steps are checked in order, first match wins. An issue older than every
	// step takes the last one.
	Steps []IssueStep
	// Now is the clock. Empty takes the store's.
	Now string
}

// IssueStep is one rung: an issue closed within ClosedWithin is asked about no
// more often than Every.
type IssueStep struct {
	ClosedWithin time.Duration
	Every        time.Duration
}

// DefaultIssueSchedule is the ladder, chosen for the shape of the risk rather
// than measured: reopening is a thing that happens within the hour, sometimes
// within the day, occasionally within the month, and hardly ever after that.
//
// The last rung is the floor rather than a cutoff — an issue closed years ago
// is still read once a week, which costs one row in one batched query.
var DefaultIssueSchedule = IssueSchedule{Steps: []IssueStep{
	{ClosedWithin: time.Hour, Every: 0},
	{ClosedWithin: 24 * time.Hour, Every: 15 * time.Minute},
	{ClosedWithin: 7 * 24 * time.Hour, Every: time.Hour},
	{ClosedWithin: 30 * 24 * time.Hour, Every: 6 * time.Hour},
	{ClosedWithin: 365 * 24 * time.Hour, Every: 24 * time.Hour},
	{ClosedWithin: 0, Every: 7 * 24 * time.Hour},
}}

// IssuesToPoll returns the issue keys worth asking the tracker about.
//
// Three sets, and each is there for a reason that would be a bug without it:
//
// Believed open, whatever its age. Stored state, never what the tracker is
// about to say: the transition into closed is itself an observation, so an
// issue that is locally open has to stay in the poll set or it closes and is
// never seen to have closed. #174 makes the same argument for pull requests.
//
// Never read at all. A linked issue starts life as a key and nothing else —
// which is most of what closing references add — and its first read is what
// gives it a state to schedule against.
//
// Believed closed and due, by the ladder above. Measured from synced_at, which
// is when the tracker was last read for it, so a poll that finds nothing still
// pushes the next one out.
func (s *Store) IssuesToPoll(ctx context.Context, tracker string, schedule IssueSchedule) ([]string, error) {
	now := schedule.Now
	if now == "" {
		now = s.now().UTC().Format(timeFormat)
	}
	at, err := time.Parse(timeFormat, now)
	if err != nil {
		return nil, fmt.Errorf("reading the clock for the issue poll: %w", err)
	}

	// Two dates per rung, both absolute: how recently it closed, and how long
	// ago it must have been read. Comparing formatted timestamps rather than
	// doing arithmetic in SQL, which is what every other query here does and
	// is safe because the format sorts the way the clock runs.
	where := []string{"closed_at IS NULL", "synced_at IS NULL"}
	args := []any{tracker}
	var previous time.Duration
	for _, step := range schedule.Steps {
		// Inclusive on the rung that means "every cycle": with Every of zero
		// the cutoff is now, and `synced_at < now` drops a row that was read
		// in this same millisecond — which is exactly what happens when a
		// sync is followed immediately by another, and is how a test that
		// closes an issue and then reopens it went intermittently green.
		clause := "synced_at < ?"
		if step.Every == 0 {
			clause = "synced_at <= ?"
		}
		stepArgs := []any{at.Add(-step.Every).Format(timeFormat)}
		if previous > 0 {
			clause = "closed_at < ? AND " + clause
			stepArgs = append([]any{at.Add(-previous).Format(timeFormat)}, stepArgs...)
		}
		if step.ClosedWithin > 0 {
			clause = "closed_at >= ? AND " + clause
			stepArgs = append([]any{at.Add(-step.ClosedWithin).Format(timeFormat)}, stepArgs...)
			previous = step.ClosedWithin
		}
		where = append(where, "("+clause+")")
		args = append(args, stepArgs...)
	}

	query := fmt.Sprintf(
		"SELECT key FROM tracker_issue WHERE tracker = ? AND (%s) ORDER BY key",
		strings.Join(where, " OR "))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("choosing which %s issues to poll: %w", tracker, err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("choosing which %s issues to poll: %w", tracker, err)
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}
