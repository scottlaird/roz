package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

func newJiraCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jira",
		Short: "Jira issues, as last observed",
		Long: "An issue is a record in its own right rather than columns on a project,\n" +
			"because one piece of work can map to more than one issue and a column\n" +
			"holds one key. Projects reference issues; use `project jira` to record\n" +
			"what Jira says, and `project link-jira` to say who is tracking it.",
	}
	cmd.AddCommand(newJiraShowCmd(), newJiraListCmd())
	return cmd
}

func newJiraShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <issue>",
		Short: "Print one issue in full",
		Args:  cobra.ExactArgs(1),
		RunE:  runJiraShow,
	}
	addOutputFlag(cmd)
	return cmd
}

func runJiraShow(cmd *cobra.Command, args []string) error {
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

	issue, err := tx.LoadJiraIssue(ctx, args[0])
	if err != nil {
		return fmt.Errorf("reading %s: %w", args[0], err)
	}
	projects, err := tx.ProjectsForJiraKey(ctx, args[0])
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
	fmt.Fprintf(w, "summary\t%s\n", orDash(issue.Summary))
	fmt.Fprintf(w, "status\t%s\n", nullText(issue.Status))
	fmt.Fprintf(w, "sprint\t%s\n", nullText(issue.Sprint))
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

func newJiraListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Every issue that has been observed or linked",
		Args:  cobra.NoArgs,
		RunE:  runJiraList,
	}
	addOutputFlag(cmd)
	return cmd
}

func runJiraList(cmd *cobra.Command, _ []string) error {
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

	issues, err := st.ListJiraIssues(ctx)
	if err != nil {
		return err
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
		fmt.Fprintln(cmd.OutOrStdout(), "no jira issues")
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tSPRINT\tASSIGNEE\tSUMMARY")
	for _, issue := range issues {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			issue.ID, nullText(issue.Status), nullText(issue.Sprint),
			nullText(issue.Assignee), orDash(issue.Summary))
	}
	return w.Flush()
}

func newProjectLinkJiraCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link-jira",
		Short: "Record that a project tracks a Jira issue",
		Long: "Idempotent, so re-running an import does not have to know what it\n" +
			"already did. The issue row is created if it is not there: a key is\n" +
			"often known before Jira has been read.\n\n" +
			"A project may track several issues. That is the whole reason this is a\n" +
			"link rather than a column — one piece of work maps to \"allow scaling\n" +
			"up\" and \"allow scaling down\", and a column holds one of them.",
		Args: cobra.NoArgs,
		RunE: runProjectLinkJira,
	}
	f := cmd.Flags()
	f.String(flagProject, "", "project, e.g. TD106 (required)")
	f.String("issue", "", "issue, e.g. CDSS-1744 (required)")
	_ = cmd.MarkFlagRequired(flagProject)
	_ = cmd.MarkFlagRequired("issue")
	addActorFlag(cmd)
	return cmd
}

func runProjectLinkJira(cmd *cobra.Command, _ []string) error {
	return runJiraLinkChange(cmd, true)
}

func newProjectUnlinkJiraCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unlink-jira",
		Short: "Stop tracking a Jira issue from a project",
		Long: "The issue itself stays. It may be tracked by another project, and what\n" +
			"Jira said about it is not made untrue by nobody watching.",
		Args: cobra.NoArgs,
		RunE: runProjectUnlinkJira,
	}
	f := cmd.Flags()
	f.String(flagProject, "", "project, e.g. TD106 (required)")
	f.String("issue", "", "issue, e.g. CDSS-1744 (required)")
	_ = cmd.MarkFlagRequired(flagProject)
	_ = cmd.MarkFlagRequired("issue")
	addActorFlag(cmd)
	return cmd
}

func runProjectUnlinkJira(cmd *cobra.Command, _ []string) error {
	return runJiraLinkChange(cmd, false)
}

func runJiraLinkChange(cmd *cobra.Command, link bool) error {
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
		err = tx.LinkProjectJira(ctx, projectID, issue)
	} else {
		err = tx.UnlinkProjectJira(ctx, projectID, issue)
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
	fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s\n", projectID, verb, issue)
	return nil
}
