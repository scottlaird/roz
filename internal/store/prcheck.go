package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Check states worth reacting to. GitHub's vocabulary is open and this is not
// a closed set — it is the subset that means somebody has to do something.
const (
	CheckFailure   = "FAILURE"
	CheckError     = "ERROR"
	CheckCancelled = "CANCELLED"
	CheckTimedOut  = "TIMED_OUT"
)

// brokenCheckStates are the conclusions that mean a check is not passing and
// will not start passing on its own.
//
// PENDING is not here: a check that has not finished is not a failure, and
// treating it as one would raise something on every push.
var brokenCheckStates = []string{CheckFailure, CheckError, CheckCancelled, CheckTimedOut}

// PRCheck is one context reported against a pull request.
//
// Not a Record: it has no identifier of its own and no lifecycle worth
// logging as created. What matters about it is transitions, and those are
// written by ApplyChecks.
type PRCheck struct {
	Name       string
	State      string
	ObservedAt string
}

// broken reports whether this check is in a state somebody has to act on.
func (c PRCheck) broken() bool {
	for _, state := range brokenCheckStates {
		if c.State == state {
			return true
		}
	}
	return false
}

// ChecksFor returns the checks reported against a pull request, by name.
func (t *Tx) ChecksFor(ctx context.Context, prID string) ([]PRCheck, error) {
	rows, err := t.tx.QueryContext(ctx,
		"SELECT name, state, observed_at FROM pr_check WHERE pr_id = ? ORDER BY name", prID)
	if err != nil {
		return nil, fmt.Errorf("reading checks for %s: %w", prID, err)
	}
	defer rows.Close()

	var checks []PRCheck
	for rows.Next() {
		var c PRCheck
		if err := rows.Scan(&c.Name, &c.State, &c.ObservedAt); err != nil {
			return nil, fmt.Errorf("reading checks for %s: %w", prID, err)
		}
		checks = append(checks, c)
	}
	return checks, rows.Err()
}

// ApplyChecks records what GitHub reported, and logs only the transitions
// worth reading.
//
// **Every transition is written. Only some are logged**, which is the whole
// point of the change from one JSON column to a row per check. That is not a
// hole in "every mutation is an event" so much as the argument `auto` columns
// already make: pr.last_synced_at is written and not logged because it moves
// on every poll and would bury the transitions that matter. A check going
// green does the same — the fifteen `checks` events an hour that prompted
// this were almost entirely that.
//
// So: crossing into a broken state is news, and crossing back out of one is
// news. A check that was already FAILURE and still is produces nothing, which
// is what finally distinguishes *newly broken* from *still broken* — the blob
// could not, because any change to any check re-emitted the lot.
//
// A check GitHub stops reporting is deleted rather than kept: contexts come
// and go with the workflow file, and a check nobody runs any more is not a
// check that failed. Its disappearance is logged only if it was broken, since
// that one is a question answered.
func (t *Tx) ApplyChecks(ctx context.Context, prID string, observed map[string]string) error {
	stored, err := t.ChecksFor(ctx, prID)
	if err != nil {
		return err
	}
	was := make(map[string]PRCheck, len(stored))
	for _, c := range stored {
		was[c.Name] = c
	}

	for _, name := range sortedNames(observed) {
		state := observed[name]
		previous, existed := was[name]
		if existed && previous.State == state {
			continue // nothing moved
		}
		if err := t.writeCheck(ctx, prID, name, state); err != nil {
			return err
		}
		now := PRCheck{Name: name, State: state}
		if previous.broken() != now.broken() {
			if err := t.logCheck(ctx, prID, name, previous.State, state); err != nil {
				return err
			}
		}
	}

	for _, c := range stored {
		if _, still := observed[c.Name]; still {
			continue
		}
		if err := t.deleteCheck(ctx, prID, c.Name); err != nil {
			return err
		}
		if c.broken() {
			if err := t.logCheck(ctx, prID, c.Name, c.State, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *Tx) writeCheck(ctx context.Context, prID, name, state string) error {
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO pr_check (pr_id, name, state, observed_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (pr_id, name) DO UPDATE SET state = excluded.state, observed_at = excluded.observed_at`,
		prID, name, state, t.at)
	if err != nil {
		return fmt.Errorf("recording check %q on %s: %w", name, prID, err)
	}
	return nil
}

func (t *Tx) deleteCheck(ctx context.Context, prID, name string) error {
	_, err := t.tx.ExecContext(ctx,
		"DELETE FROM pr_check WHERE pr_id = ? AND name = ?", prID, name)
	if err != nil {
		return fmt.Errorf("removing check %q from %s: %w", name, prID, err)
	}
	return nil
}

// logCheck writes the event for a check crossing into or out of trouble.
//
// The field is the check's name, so the log reads the way a column change
// does — `checks/build: "SUCCESS" → "FAILURE"` — and `roz watch` needs to
// know nothing new to print it.
func (t *Tx) logCheck(ctx context.Context, prID, name, from, to string) error {
	return t.Changed(ctx, &PR{ID: prID}, "checks/"+name, from, to)
}

func sortedNames(checks map[string]string) []string {
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// FormatChecks renders the checks for display, worst first: what is broken is
// what a reader is looking for.
func FormatChecks(checks []PRCheck) string {
	if len(checks) == 0 {
		return ""
	}
	sorted := append([]PRCheck(nil), checks...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].broken() != sorted[j].broken() {
			return sorted[i].broken()
		}
		return sorted[i].Name < sorted[j].Name
	})

	parts := make([]string, len(sorted))
	for i, c := range sorted {
		parts[i] = c.Name + " " + c.State
	}
	return strings.Join(parts, ", ")
}
