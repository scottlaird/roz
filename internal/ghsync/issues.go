package ghsync

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/scottlaird/roz/internal/github"
	"github.com/scottlaird/roz/internal/store"
)

// IssueReader reads tracked GitHub issues.
//
// Optional, like ChangeReader: a client that cannot answer simply does not,
// and everything else about a sync carries on. It is separate from Fetcher
// because an issue is a different object from a pull request even though
// GitHub numbers them from one sequence.
type IssueReader interface {
	Issues(ctx context.Context, keys []string) (github.IssueResult, error)
}

// eventIssueClosedWithWork is raised when an issue closes while work against
// it is still open.
const eventIssueClosedWithWork = "issue_closed_with_open_actions"

// syncIssues refreshes what GitHub says about the issues roz tracks.
//
// The schema has carried a tracker discriminator since 0016, so a GitHub issue
// could always be *recorded*; nothing ever read one, so its title and state
// were whatever was last typed into `roz issue observe`. This is the half that
// was missing.
//
// Written through ObserveTrackerIssues, the same path the hand-run command
// uses. A poll and a person typing are the same kind of act — an observation
// about somebody else's tracker — and the only thing that should differ is the
// actor recorded against it.
func syncIssues(ctx context.Context, st *store.Store, reader IssueReader, result *Result) error {
	if reader == nil {
		return nil
	}
	// Not every recorded issue: what is open, what has never been read, and
	// what is closed and due. A pull request's closing references add an issue
	// per reference and nothing untracks a row, so a flat poll grew without
	// bound — see IssuesToPoll.
	keys, err := st.IssuesToPoll(ctx, store.TrackerGitHub, store.DefaultIssueSchedule)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	result.IssuesPolled = len(keys)

	fetched, err := reader.Issues(ctx, keys)
	if err != nil {
		return &readError{err: err}
	}
	result.asked++
	if fetched.RateLimit.Known() {
		result.RateLimit = fetched.RateLimit
	}

	observations := make([]store.TrackerObservation, 0, len(fetched.Issues))
	for _, issue := range fetched.Issues {
		observations = append(observations, observationOf(issue))
	}

	applied, err := st.ObserveTrackerIssues(ctx, store.ActorSyncGitHub, observations)
	if err != nil {
		return err
	}
	for _, one := range applied.Applied {
		if reported := worthReporting(one); len(reported.Changes) > 0 {
			result.Issues = append(result.Issues, reported)
		}
		if closedNow(one) {
			raised, err := reportIssueClosed(ctx, st, one)
			if err != nil {
				return err
			}
			if raised != "" {
				result.IssuesClosedWithWork = append(result.IssuesClosedWithWork, raised)
			}
		}
	}

	for key, why := range fetched.Missing {
		if err := reportIssueUnreadable(ctx, st, key, why); err != nil {
			return err
		}
	}
	return nil
}

// worthReporting drops the changes a poll makes merely by happening.
//
// synced_at moves every time an issue is read, so a syncer left running would
// otherwise print a line per issue per poll and say nothing by it. The column
// is still written and still logged — when an issue was last read is worth
// knowing — it is just not news.
func worthReporting(applied store.TrackerApplied) store.TrackerApplied {
	changes := make([]store.Change, 0, len(applied.Changes))
	for _, change := range applied.Changes {
		if change.Column != "synced_at" {
			changes = append(changes, change)
		}
	}
	applied.Changes = changes
	return applied
}

// observationOf turns what GitHub said into what the store records.
//
// The status is GitHub's own word, not one of ours. The column is deliberately
// unconstrained because a tracker may add a value whenever it likes, and
// translating OPEN into something roz prefers would be inventing a vocabulary
// nobody else uses.
func observationOf(issue github.Issue) store.TrackerObservation {
	return store.TrackerObservation{
		Tracker:   store.TrackerGitHub,
		Key:       issue.Key,
		Summary:   sql.NullString{String: issue.Title, Valid: issue.Title != ""},
		Status:    sql.NullString{String: issue.State, Valid: issue.State != ""},
		Iteration: sql.NullString{String: issue.Milestone, Valid: issue.Milestone != ""},
		ClosedAt:  sql.NullString{String: issue.ClosedAt, Valid: issue.ClosedAt != ""},
		// Every assignee, not the first. The column holds a name and usually
		// gets one, but picking among several would be a guess about which
		// mattered, and joining them is at least true.
		Assignee: sql.NullString{
			String: strings.Join(issue.Assignees, ", "),
			Valid:  len(issue.Assignees) > 0,
		},
	}
}

// closedNow reports whether this poll is the one that saw the issue close.
//
// Read from the change rather than from the state, so it fires on the
// transition: an issue that stays closed is not news again.
//
// The first observation of an issue is not a transition either, even though it
// writes a status. Roz has no idea when an issue it is seeing for the first
// time was closed, and the case this produces is linking a project to an issue
// that was closed long ago — where the message would read as though something
// had just happened. So a closure needs a status roz saw before this one.
func closedNow(applied store.TrackerApplied) bool {
	for _, change := range applied.Changes {
		if change.Column == "status" {
			return change.Old != "" &&
				strings.EqualFold(change.New, issueClosed) &&
				!strings.EqualFold(change.Old, issueClosed)
		}
	}
	return false
}

// issueClosed is GitHub's word for it.
const issueClosed = "CLOSED"

// reportIssueClosed says that an issue closed while work against it is still
// open, and returns the issue it reported.
//
// Only where there is open work: an issue closing with nothing left against it
// is the ordinary, happy case and worth nothing at all. What this catches is
// the other one — somebody closed the ticket and the queue still thinks there
// is something to do, which means either the queue is stale or the ticket was
// closed early.
//
// An exception rather than an action, for now. Which of those two it is
// depends on the project, and raising a decision every time would be right for
// one of them and noise for the other.
func reportIssueClosed(ctx context.Context, st *store.Store, applied store.TrackerApplied) (string, error) {
	open, err := st.OpenActionsForIssue(ctx, applied.ID())
	if err != nil {
		return "", err
	}
	if len(open) == 0 {
		return "", nil
	}

	tx, err := st.Begin(ctx, store.ActorSyncGitHub)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	subject, err := tx.LoadTrackerIssue(ctx, applied.ID())
	if err != nil {
		return "", nil
	}
	note := fmt.Sprintf("%s closed with %s still open",
		applied.Key, strings.Join(open, ", "))
	// Once a day: the issue stays closed and the actions stay open, so this
	// is a standing condition rather than an event that repeats.
	if _, err := tx.ExceptionOnce(ctx, subject, eventIssueClosedWithWork, note); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return note, nil
}

// eventIssueUnreadable is raised when a tracked issue cannot be read.
const eventIssueUnreadable = "issue_unresolvable"

// reportIssueUnreadable logs an exception against an issue GitHub would not
// resolve. It changes nothing: whether to stop tracking it is a judgement.
//
// The commonest cause is a key naming a pull request rather than an issue.
// GitHub numbers them from one sequence, so the mistake is easy and its
// symptom — nothing ever updating — is silent without this.
func reportIssueUnreadable(ctx context.Context, st *store.Store, key, why string) error {
	tx, err := st.Begin(ctx, store.ActorSyncGitHub)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	subject, err := tx.LoadTrackerIssue(ctx, store.IssueID(store.TrackerGitHub, key))
	if err != nil {
		return nil
	}
	if _, err := tx.ExceptionOnce(ctx, subject, eventIssueUnreadable,
		fmt.Sprintf("%s could not be read: %s", key, why)); err != nil {
		return err
	}
	return tx.Commit()
}
