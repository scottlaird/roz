package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// Where a pull-request-to-issue link came from.
//
// Not an actor: no link table carries one, because who linked is in the event
// log. This says whose row it is, which is a question about deletion — sync
// reconciles what GitHub reports and must be able to drop its own rows without
// touching a person's. See 0038.
const (
	// LinkFromGitHub is a closingIssuesReferences entry: GitHub parsed a
	// closing keyword out of the body and will close that issue on merge.
	LinkFromGitHub = "github"
	// LinkFromManual is somebody saying so. The only path for Jira, which
	// nothing reads, and the way a wrong parse is corrected.
	LinkFromManual = "manual"
)

// ValidateLinkSource checks a source against the CHECK constraint, so a bad
// one is reported by name rather than as a constraint failure.
func ValidateLinkSource(source string) error {
	switch source {
	case LinkFromGitHub, LinkFromManual:
		return nil
	default:
		return fmt.Errorf("link source %q is not recognised: use %s or %s",
			source, LinkFromGitHub, LinkFromManual)
	}
}

// LinkPRIssue records that a pull request is against an issue. Idempotent, for
// the reason LinkProjectIssue is: a caller re-running an import should not
// have to know what it already did.
//
// The issue row is created if it is not there, which is not a convenience.
// IssueKeys drives the poll from tracker_issue, so an issue nothing has
// recorded is never read — a link to one would point at a row that never
// updates. The same reasoning WaitOnIssue is built on.
func (t *Tx) LinkPRIssue(ctx context.Context, prID, tracker, key, source string) error {
	if err := ValidateTracker(tracker); err != nil {
		return err
	}
	if err := ValidateLinkSource(source); err != nil {
		return err
	}
	id := IssueID(tracker, key)
	if err := t.ensureIssue(ctx, id, tracker, key); err != nil {
		return err
	}
	return t.linkPRIssueByID(ctx, prID, id, source)
}

func (t *Tx) linkPRIssueByID(ctx context.Context, prID, issueID, source string) error {
	res, err := t.tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO pr_tracker_issue (pr_id, issue_id, source, created_at)
		VALUES (?, ?, ?, ?)`, prID, issueID, source, t.at)
	if err != nil {
		return fmt.Errorf("linking %s to %s: %w", prID, issueID, err)
	}
	// Silent when the row was already there. Re-running an import is not an
	// event, and a sync that logged one every cycle would drown the log in
	// facts that have not changed.
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	return t.emit(ctx, &PR{ID: prID}, event{
		kind: eventLinked, field: "issue", newValue: issueID, note: source,
	})
}

// UnlinkPRIssue removes a manual link, leaving the issue alone.
//
// It refuses to remove GitHub's. That row is not an opinion this can overrule:
// sync would put it back on the next cycle, and an unlink that silently undid
// itself is worse than one that says why. The place to change it is the pull
// request body, which is where GitHub read it from.
func (t *Tx) UnlinkPRIssue(ctx context.Context, prID, tracker, key string) error {
	id := IssueID(tracker, key)
	res, err := t.tx.ExecContext(ctx,
		"DELETE FROM pr_tracker_issue WHERE pr_id = ? AND issue_id = ? AND source = ?",
		prID, id, LinkFromManual)
	if err != nil {
		return fmt.Errorf("unlinking %s from %s: %w", prID, id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var fromGitHub int
		if err := t.tx.QueryRowContext(ctx,
			"SELECT count(*) FROM pr_tracker_issue WHERE pr_id = ? AND issue_id = ? AND source = ?",
			prID, id, LinkFromGitHub).Scan(&fromGitHub); err != nil {
			return err
		}
		if fromGitHub > 0 {
			return fmt.Errorf(
				"%s is linked to %s by GitHub, which sync would restore: "+
					"edit the pull request body to drop the closing keyword", prID, id)
		}
		return fmt.Errorf("%s is not linked to %s", prID, id)
	}
	return t.emit(ctx, &PR{ID: prID}, event{
		kind: eventUnlinked, field: "issue", oldValue: id, note: LinkFromManual,
	})
}

// ReconcileClosingIssues makes the github rows for one pull request match what
// GitHub reported, and touches nothing else.
//
// Reconciliation rather than accumulation: a closing keyword removed from a
// body is GitHub saying the pull request no longer closes that issue, and a
// link that could only ever be added would keep saying otherwise for ever.
//
// Scoped to source = 'github' throughout. A person's link to the same issue
// survives sync dropping its own, which is the reason source is in the primary
// key rather than beside it.
//
// keys are issue keys in GitHub's own form, owner/repo#number, which is what
// closingIssuesReferences reports and covers issues in other repositories.
func (t *Tx) ReconcileClosingIssues(ctx context.Context, prID string, keys []string) error {
	wanted := make(map[string]string, len(keys))
	for _, key := range keys {
		wanted[IssueID(TrackerGitHub, key)] = key
	}

	held, err := t.issueStrings(ctx,
		"SELECT issue_id FROM pr_tracker_issue WHERE pr_id = ? AND source = ? ORDER BY issue_id",
		prID, LinkFromGitHub)
	if err != nil {
		return err
	}

	stored := make(map[string]bool, len(held))
	for _, id := range held {
		stored[id] = true
		if _, keep := wanted[id]; keep {
			continue
		}
		if _, err := t.tx.ExecContext(ctx,
			"DELETE FROM pr_tracker_issue WHERE pr_id = ? AND issue_id = ? AND source = ?",
			prID, id, LinkFromGitHub); err != nil {
			return fmt.Errorf("unlinking %s from %s: %w", prID, id, err)
		}
		if err := t.emit(ctx, &PR{ID: prID}, event{
			kind: eventUnlinked, field: "issue", oldValue: id, note: LinkFromGitHub,
		}); err != nil {
			return err
		}
	}

	// Sorted, so the events of a first sync read in a stable order rather
	// than in whatever order the API listed them.
	ids := make([]string, 0, len(wanted))
	for id := range wanted {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		if stored[id] {
			continue
		}
		if err := t.ensureIssue(ctx, id, TrackerGitHub, wanted[id]); err != nil {
			return err
		}
		if err := t.linkPRIssueByID(ctx, prID, id, LinkFromGitHub); err != nil {
			return err
		}
	}
	return nil
}

// ensureIssue records an issue key that nothing has recorded yet.
//
// Linking is what puts an issue into the poll — IssueKeys reads tracker_issue
// — so this is what makes the link point at something that will ever update.
func (t *Tx) ensureIssue(ctx context.Context, id, tracker, key string) error {
	if _, err := t.LoadTrackerIssue(ctx, id); err != nil {
		if err != sql.ErrNoRows {
			return err
		}
		return t.Insert(ctx, NewTrackerIssue(tracker, key))
	}
	return nil
}

// IssueIDsForPR returns the issues a pull request is against, in id order so a
// listing is stable. An issue linked both ways appears once.
func (t *Tx) IssueIDsForPR(ctx context.Context, prID string) ([]string, error) {
	return t.issueStrings(ctx,
		"SELECT DISTINCT issue_id FROM pr_tracker_issue WHERE pr_id = ? ORDER BY issue_id", prID)
}

// PRIDsForIssue is the direction the weekly wrap-up reads: which pull requests
// were against this issue.
func (t *Tx) PRIDsForIssue(ctx context.Context, issueID string) ([]string, error) {
	return t.issueStrings(ctx,
		"SELECT DISTINCT pr_id FROM pr_tracker_issue WHERE issue_id = ? ORDER BY pr_id", issueID)
}
