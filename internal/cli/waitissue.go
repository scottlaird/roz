package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

func newActionWaitIssueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wait-issue",
		Short: "Say which tracker issue an action is waiting for",
		Long: "For work blocked on something that is not ours: an upstream bug, a\n" +
			"ticket another team owns, an issue in somebody else's repository.\n\n" +
			"  roz action wait-issue --action NA103 --tracker github --issue rust-lang/rust#1\n\n" +
			"Without this, waiting on an issue is a plain `wait` nobody can close\n" +
			"except by hand — so it sits in the queue looking live until somebody\n" +
			"happens to notice the issue closed.\n\n" +
			"The issue is recorded if it is not already, which is also what makes\n" +
			"it polled: sync reads the issues roz has rows for, so a wait on one\n" +
			"nothing had recorded would never come true.\n\n" +
			"It closes on the tracker's own closing time rather than on its\n" +
			"status, since 'Done', 'Closed' and 'Resolved' are three trackers'\n" +
			"names for one idea. GitHub supplies that time on every sync; Jira has\n" +
			"it only where `roz issue observe --closed-at` was given one.\n\n" +
			"One issue per action. Waiting on two is two waits, which the blocking\n" +
			"graph says better — it can say which arrived first.",
		Args: cobra.NoArgs,
		RunE: runActionWaitIssue,
	}
	cmd.Flags().String(flagAction, "", "action, e.g. NA103 (required)")
	cmd.Flags().String(flagIssueKey, "", "the tracker's own key, e.g. CDSS-1744 (required)")
	_ = cmd.MarkFlagRequired(flagAction)
	_ = cmd.MarkFlagRequired(flagIssueKey)
	addTrackerFlag(cmd)
	addActorFlag(cmd)
	return cmd
}

func runActionWaitIssue(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()

	id, err := f.GetString(flagAction)
	if err != nil {
		return err
	}
	key, err := f.GetString(flagIssueKey)
	if err != nil {
		return err
	}
	tracker, err := f.GetString(flagTracker)
	if err != nil {
		return err
	}
	if err := store.ValidateTracker(tracker); err != nil {
		return err
	}

	return withActionTxThen(cmd,
		func(ctx context.Context, tx *store.Tx) error {
			a, err := loadAction(ctx, tx, id)
			if err != nil {
				return err
			}
			if err := tx.WaitOnIssue(ctx, a.ID, tracker, key); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s waits for %s\n",
				a.ID, store.IssueID(tracker, key))
			return nil
		},
		// The issue may already be closed: a wait written after the fact is
		// satisfied the moment it is recorded, rather than at the next poll.
		// An issue nothing has read settles nothing, and sync does it — which
		// is the ordinary case, since linking is usually what starts it being
		// polled at all.
		func(ctx context.Context, st *store.Store) error {
			return settleActionNow(ctx, cmd, st, id)
		})
}
