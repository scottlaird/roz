package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Bodies of work, allocated an SL identifier",
	}
	cmd.AddCommand(
		newProjectAddCmd(),
		newProjectSupersedeCmd(),
		newProjectListCmd(),
	)
	return cmd
}

// Flag names for the authored columns settable at creation. superseded_by is
// not among them: use `project supersede`, which records both ends.
const (
	flagTitle        = "title"
	flagSummary      = "summary"
	flagStatus       = "status"
	flagPriority     = "priority"
	flagEffort       = "effort"
	flagSnoozeUntil  = "snooze-until"
	flagSnoozeReason = "snooze-reason"
	flagDesignRef    = "design-ref"
	flagJiraKey      = "jira-key"
	flagJSON         = "json"
)

func newProjectAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Allocate a new project and print its id",
		Long: "Authored columns can be set with the flags below or with --json, keyed\n" +
			"by column name. --json is applied first, so an explicit flag overrides\n" +
			"it; that makes --json a convenient base for an agent to generate.\n\n" +
			"Observed columns cannot be set either way — they are sync's to write.",
		Args: cobra.NoArgs,
		RunE: runProjectAdd,
	}

	f := cmd.Flags()
	f.String(flagTitle, "", "what the project is (required)")
	f.String(flagSummary, "", "how it relates to other items; not a design note")
	f.String(flagStatus, "", "active, blocked, snoozed, done, retired or superseded")
	f.Int(flagPriority, 0, "1 to 4")
	f.String(flagEffort, "", "minutes, hours, session, days or weeks")
	f.String(flagSnoozeUntil, "", "ISO-8601 date or timestamp; requires status snoozed")
	f.String(flagSnoozeReason, "", "why it is deferred")
	f.StringArray(flagDesignRef, nil, "path to a design note; repeatable")
	f.String(flagJiraKey, "", "e.g. CDSS-1744")
	f.String(flagJSON, "", "remaining authored columns as a JSON object, keyed by column name")
	_ = cmd.MarkFlagRequired(flagTitle)
	addActorFlag(cmd)

	return cmd
}

func runProjectAdd(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	title, err := cmd.Flags().GetString(flagTitle)
	if err != nil {
		return err
	}

	// Build and validate before allocating: the number is consumed as soon as
	// AllocateProject returns, so rejected input should not burn one.
	p := store.NewProject(title)
	if err := applyProjectJSON(cmd, p); err != nil {
		return err
	}
	if err := applyProjectFlags(cmd, p); err != nil {
		return err
	}
	if err := st.AllocateProject(ctx, p); err != nil {
		return err
	}

	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := tx.Insert(ctx, p); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout(), p.ID)
	return nil
}

func applyProjectJSON(cmd *cobra.Command, p *store.Project) error {
	raw, err := cmd.Flags().GetString(flagJSON)
	if err != nil || raw == "" {
		return err
	}
	return store.ApplyJSON(p, []byte(raw))
}

// applyProjectFlags sets only the columns whose flags were given, so an unset
// flag never overwrites a value that came from --json.
func applyProjectFlags(cmd *cobra.Command, p *store.Project) error {
	f := cmd.Flags()

	if f.Changed(flagSummary) {
		v, err := f.GetString(flagSummary)
		if err != nil {
			return err
		}
		p.Summary = v
	}
	if f.Changed(flagStatus) {
		v, err := f.GetString(flagStatus)
		if err != nil {
			return err
		}
		p.Status = v
	}
	if f.Changed(flagPriority) {
		v, err := f.GetInt(flagPriority)
		if err != nil {
			return err
		}
		p.Priority = sql.NullInt64{Int64: int64(v), Valid: true}
	}
	if f.Changed(flagEffort) {
		v, err := f.GetString(flagEffort)
		if err != nil {
			return err
		}
		p.Effort = sql.NullString{String: v, Valid: true}
	}
	if f.Changed(flagSnoozeUntil) {
		v, err := f.GetString(flagSnoozeUntil)
		if err != nil {
			return err
		}
		p.SnoozeUntil = sql.NullString{String: v, Valid: true}
	}
	if f.Changed(flagSnoozeReason) {
		v, err := f.GetString(flagSnoozeReason)
		if err != nil {
			return err
		}
		p.SnoozeReason = v
	}
	if f.Changed(flagJiraKey) {
		v, err := f.GetString(flagJiraKey)
		if err != nil {
			return err
		}
		p.JiraKey = sql.NullString{String: v, Valid: true}
	}
	if f.Changed(flagDesignRef) {
		refs, err := f.GetStringArray(flagDesignRef)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(refs)
		if err != nil {
			return err
		}
		p.DesignRefs = string(encoded)
	}
	return nil
}

func newProjectSupersedeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "supersede",
		Short: "Record that one project is the same work as another",
		Long: "The superseded project keeps its identifier and stops rendering; the\n" +
			"edge survives so old references still resolve.",
		Args: cobra.NoArgs,
		Run:  stub,
	}
	f := cmd.Flags()
	f.String("from", "", "project being superseded, e.g. SL32 (required)")
	f.String("into", "", "project that replaces it, e.g. SL94 (required)")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("into")
	addActorFlag(cmd)
	return cmd
}

func newProjectListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "The projects table",
		Args:  cobra.NoArgs,
		RunE:  runProjectList,
	}
	f := cmd.Flags()
	f.Bool("orphaned", false, "no open action and no snooze — how live work goes quiet")
	f.Bool("expired", false, "snoozed with a date that has passed")
	f.String("status", "", "filter to one status")
	addOutputFlag(cmd)
	return cmd
}

func runProjectList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	filter, err := projectFilterFrom(cmd)
	if err != nil {
		return err
	}
	projects, err := st.ListProjects(ctx, filter)
	if err != nil {
		return err
	}

	if format == outputJSON {
		return writeProjectJSON(cmd.OutOrStdout(), projects)
	}
	return writeProjectTable(cmd.OutOrStdout(), projects)
}

// writeProjectJSON emits an array, empty rather than null when there is
// nothing, so a consumer can iterate without a nil check.
func writeProjectJSON(out io.Writer, projects []*store.Project) error {
	encoded, err := store.MarshalRecords(projects)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(encoded))
	return err
}

func projectFilterFrom(cmd *cobra.Command) (store.ProjectFilter, error) {
	f := cmd.Flags()

	status, err := f.GetString("status")
	if err != nil {
		return store.ProjectFilter{}, err
	}
	expired, err := f.GetBool("expired")
	if err != nil {
		return store.ProjectFilter{}, err
	}
	orphaned, err := f.GetBool("orphaned")
	if err != nil {
		return store.ProjectFilter{}, err
	}
	return store.ProjectFilter{Status: status, Expired: expired, Orphaned: orphaned}, nil
}

func writeProjectTable(out io.Writer, projects []*store.Project) error {
	if len(projects) == 0 {
		fmt.Fprintln(out, "no projects")
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tPRI\tEFFORT\tSNOOZED UNTIL\tTITLE")
	for _, p := range projects {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			p.ID, p.Status, nullIntText(p.Priority), nullText(p.Effort),
			nullText(p.SnoozeUntil), p.Title)
	}
	return w.Flush()
}

// nullText renders an absent value as a dash, which reads better in a table
// than a blank column.
func nullText(v sql.NullString) string {
	if !v.Valid || v.String == "" {
		return "-"
	}
	return v.String
}

func nullIntText(v sql.NullInt64) string {
	if !v.Valid {
		return "-"
	}
	return fmt.Sprint(v.Int64)
}
