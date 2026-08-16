package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

func newPRLinkIssueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link-issue <repo#number>",
		Short: "Record that a pull request is against a tracker issue",
		Long: "Sync already records what GitHub reports: a `Closes #123` in the body\n" +
			"becomes a link on the next `roz sync github`, and one taken back out\n" +
			"of the body takes its link with it.\n\n" +
			"This is for the rest. A Jira key is not in a field GitHub reads, so\n" +
			"nothing will ever observe it; \"part of\" is a real association that\n" +
			"GitHub does not report as a closing one; and a link that should not\n" +
			"be there is corrected here.\n\n" +
			"A link made here is a person's, and sync will not remove it. That is\n" +
			"what makes it worth typing: it survives the pull request body being\n" +
			"rewritten.",
		Args: cobra.ExactArgs(1),
		RunE: runPRLinkIssue,
	}
	addPRLinkFlags(cmd)
	return cmd
}

func newPRUnlinkIssueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unlink-issue <repo#number>",
		Short: "Remove a hand-made link between a pull request and an issue",
		Long: "Only removes a link somebody made. GitHub's own — the ones behind\n" +
			"auto-close — are refused rather than removed, because sync would put\n" +
			"one back on the next cycle and an unlink that undid itself would be\n" +
			"worse than one that says why. The place to change that is the pull\n" +
			"request body.\n\n" +
			"The issue itself stays. Another pull request may be against it, and\n" +
			"what the tracker said is not made untrue by nobody pointing at it.",
		Args: cobra.ExactArgs(1),
		RunE: runPRUnlinkIssue,
	}
	addPRLinkFlags(cmd)
	return cmd
}

func addPRLinkFlags(cmd *cobra.Command) {
	cmd.Flags().String(flagIssueKey, "",
		"the tracker's own key, e.g. CDSS-1744 or owner/repo#123 (required)")
	_ = cmd.MarkFlagRequired(flagIssueKey)
	addTrackerFlag(cmd)
	addActorFlag(cmd)
}

func runPRLinkIssue(cmd *cobra.Command, args []string) error {
	return runPRIssueLink(cmd, args[0], true)
}

func runPRUnlinkIssue(cmd *cobra.Command, args []string) error {
	return runPRIssueLink(cmd, args[0], false)
}

func runPRIssueLink(cmd *cobra.Command, prID string, link bool) error {
	ctx := cmd.Context()

	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	key, err := cmd.Flags().GetString(flagIssueKey)
	if err != nil {
		return err
	}
	tracker, err := cmd.Flags().GetString(flagTracker)
	if err != nil {
		return err
	}
	if err := store.ValidateTracker(tracker); err != nil {
		return err
	}

	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Loading the pull request first, so an untracked one is refused by name
	// rather than by a foreign key.
	if _, err := tx.LoadPR(ctx, prID); err != nil {
		return fmt.Errorf("reading %s: %w", prID, err)
	}
	if link {
		err = tx.LinkPRIssue(ctx, prID, tracker, key, store.LinkFromManual)
	} else {
		err = tx.UnlinkPRIssue(ctx, prID, tracker, key)
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	verb := "→"
	if !link {
		verb = "✕"
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s\n",
		prID, verb, store.IssueID(tracker, key))
	return err
}
