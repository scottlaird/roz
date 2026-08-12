package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

func newIssueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "issue",
		Aliases: []string{"jira"},
		Short:   "Tracker issues, as last observed",
		Long: "An issue is a record in its own right rather than columns on a project,\n" +
			"because one piece of work can map to more than one issue and a column\n" +
			"holds one key. Projects reference issues; use `issue observe` to record\n" +
			"what a tracker says, and `project link-issue` to say who is tracking it.\n\n" +
			"An issue is identified by its tracker and the tracker's own key, written\n" +
			"together as jira:CDSS-1744. Commands that take one accept either that,\n" +
			"or a bare key with --tracker.",
	}
	cmd.AddCommand(newIssueShowCmd(), newIssueListCmd(), newIssueObserveCmd())
	return cmd
}

// issueIDFrom resolves what the user typed into a composed id.
//
// A bare key takes --tracker; anything already carrying a tracker is used as
// written. No tracker's own keys contain a colon, so the two cannot be
// confused for one another.
func issueIDFrom(cmd *cobra.Command, arg string) (string, error) {
	if tracker, _, found := strings.Cut(arg, ":"); found {
		return arg, store.ValidateTracker(tracker)
	}
	tracker, err := cmd.Flags().GetString(flagTracker)
	if err != nil {
		return "", err
	}
	if err := store.ValidateTracker(tracker); err != nil {
		return "", err
	}
	return store.IssueID(tracker, arg), nil
}

func newIssueShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <issue>",
		Short: "Print one issue in full",
		Args:  cobra.ExactArgs(1),
		RunE:  runIssueShow,
	}
	addTrackerFlag(cmd)
	addOutputFlag(cmd)
	return cmd
}

func runIssueShow(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	id, err := issueIDFrom(cmd, args[0])
	if err != nil {
		return err
	}
	issue, err := tx.LoadTrackerIssue(ctx, id)
	if err != nil {
		return fmt.Errorf("reading %s: %w", id, err)
	}
	projects, err := tx.ProjectsForIssue(ctx, id)
	if err != nil {
		return err
	}

	if format == outputJSON {
		encoded, err := store.MarshalRecord(issue)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "id\t%s\n", issue.ID)
	fmt.Fprintf(w, "tracker\t%s\n", issue.Tracker)
	fmt.Fprintf(w, "key\t%s\n", issue.Key)
	fmt.Fprintf(w, "summary\t%s\n", orDash(issue.Summary))
	fmt.Fprintf(w, "status\t%s\n", nullText(issue.Status))
	fmt.Fprintf(w, "iteration\t%s\n", nullText(issue.Iteration))
	fmt.Fprintf(w, "assignee\t%s\n", nullText(issue.Assignee))
	fmt.Fprintf(w, "synced_at\t%s\n", nullText(issue.SyncedAt))
	if len(projects) == 0 {
		fmt.Fprintf(w, "tracked by\t-\n")
	}
	for _, id := range projects {
		fmt.Fprintf(w, "tracked by\t%s\n", id)
	}
	return w.Flush()
}

func newIssueListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Every issue that has been observed or linked",
		Args:  cobra.NoArgs,
		RunE:  runIssueList,
	}
	cmd.Flags().String(flagTracker, "", "keep only one tracker's issues")
	addOutputFlag(cmd)
	return cmd
}

func runIssueList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	tracker, err := cmd.Flags().GetString(flagTracker)
	if err != nil {
		return err
	}
	if tracker != "" {
		if err := store.ValidateTracker(tracker); err != nil {
			return err
		}
	}

	issues, err := st.ListTrackerIssues(ctx)
	if err != nil {
		return err
	}
	if tracker != "" {
		kept := issues[:0]
		for _, issue := range issues {
			if issue.Tracker == tracker {
				kept = append(kept, issue)
			}
		}
		issues = kept
	}

	if format == outputJSON {
		encoded, err := store.MarshalRecords(issues)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}

	if len(issues) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no issues")
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tITERATION\tASSIGNEE\tSUMMARY")
	for _, issue := range issues {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			issue.ID, nullText(issue.Status), nullText(issue.Iteration),
			nullText(issue.Assignee), orDash(issue.Summary))
	}
	return w.Flush()
}

func newProjectLinkIssueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "link-issue",
		Aliases: []string{"link-jira"},
		Short:   "Record that a project tracks a tracker issue",
		Long: "Idempotent, so re-running an import does not have to know what it\n" +
			"already did. The issue row is created if it is not there: a key is\n" +
			"often known before the tracker has been read.\n\n" +
			"A project may track several issues, and from more than one tracker.\n" +
			"That is the whole reason this is a link rather than a column — one\n" +
			"piece of work maps to \"allow scaling up\" and \"allow scaling down\",\n" +
			"and a column holds one of them.",
		Args: cobra.NoArgs,
		RunE: runProjectLinkIssue,
	}
	addLinkFlags(cmd)
	return cmd
}

func addLinkFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String(flagProject, "", "project, e.g. ROZ106 (required)")
	f.String("issue", "", "the tracker's own key, e.g. CDSS-1744 (required)")
	_ = cmd.MarkFlagRequired(flagProject)
	_ = cmd.MarkFlagRequired("issue")
	addTrackerFlag(cmd)
	addActorFlag(cmd)
}

func runProjectLinkIssue(cmd *cobra.Command, _ []string) error {
	return runIssueLinkChange(cmd, true)
}

func newProjectUnlinkIssueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "unlink-issue",
		Aliases: []string{"unlink-jira"},
		Short:   "Stop tracking a tracker issue from a project",
		Long: "The issue itself stays. It may be tracked by another project, and what\n" +
			"the tracker said about it is not made untrue by nobody watching.",
		Args: cobra.NoArgs,
		RunE: runProjectUnlinkIssue,
	}
	addLinkFlags(cmd)
	return cmd
}

func runProjectUnlinkIssue(cmd *cobra.Command, _ []string) error {
	return runIssueLinkChange(cmd, false)
}

func runIssueLinkChange(cmd *cobra.Command, link bool) error {
	ctx := cmd.Context()

	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	projectID, err := cmd.Flags().GetString(flagProject)
	if err != nil {
		return err
	}
	issue, err := cmd.Flags().GetString("issue")
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

	// Loading the project first so an unknown one is refused by name rather
	// than by a foreign key.
	if _, err := tx.LoadProject(ctx, projectID); err != nil {
		return fmt.Errorf("reading %s: %w", projectID, err)
	}
	if link {
		err = tx.LinkProjectIssue(ctx, projectID, tracker, issue)
	} else {
		err = tx.UnlinkProjectIssue(ctx, projectID, tracker, issue)
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	verb := "tracks"
	if !link {
		verb = "no longer tracks"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s\n", projectID, verb, store.IssueID(tracker, issue))
	return nil
}
