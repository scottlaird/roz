package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
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
		newProjectShowCmd(),
		newProjectSetCmd(),
		newProjectSnoozeCmd(),
		newProjectWakeCmd(),
		newProjectSupersedeCmd(),
		newProjectCloseCmd(),
		newProjectListCmd(),
		newProjectJiraCmd(),
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

	addProjectFieldFlags(cmd)
	_ = cmd.MarkFlagRequired(flagTitle)
	addActorFlag(cmd)

	return cmd
}

// projectFieldFlags are the authored columns settable from the command line,
// in help order. superseded_by is absent on purpose: `project supersede`
// records it with the checks that belong to it.
var projectFieldFlags = []string{
	flagTitle, flagSummary, flagStatus, flagPriority, flagEffort,
	flagSnoozeUntil, flagSnoozeReason, flagDesignRef, flagJiraKey,
}

func addProjectFieldFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String(flagTitle, "", "what the project is")
	f.String(flagSummary, "", "how it relates to other items; not a design note")
	f.String(flagStatus, "", "active, blocked, snoozed, done, retired or superseded")
	f.Int(flagPriority, 0, "1 to 4")
	f.String(flagEffort, "", "minutes, hours, session, days or weeks")
	f.String(flagSnoozeUntil, "", "ISO-8601 date or timestamp; requires status snoozed")
	f.String(flagSnoozeReason, "", "why it is deferred")
	f.StringArray(flagDesignRef, nil, "path to a design note; repeatable")
	f.String(flagJiraKey, "", "e.g. CDSS-1744")
	f.String(flagJSON, "", "authored columns as a JSON object, keyed by column name")
}

// anyProjectFieldGiven reports whether the invocation asked for any change at
// all, so `project set` with no flags can say so rather than silently doing
// nothing.
func anyProjectFieldGiven(cmd *cobra.Command) bool {
	for _, flag := range append(projectFieldFlags, flagJSON) {
		if cmd.Flags().Changed(flag) {
			return true
		}
	}
	return false
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

	if f.Changed(flagTitle) {
		v, err := f.GetString(flagTitle)
		if err != nil {
			return err
		}
		p.Title = v
	}
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
		p.Effort = nullString(v)
	}
	if f.Changed(flagSnoozeUntil) {
		v, err := f.GetString(flagSnoozeUntil)
		if err != nil {
			return err
		}
		p.SnoozeUntil = nullString(v)
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
		p.JiraKey = nullString(v)
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

// nullString maps an empty flag value to NULL rather than to an empty
// string. The two are indistinguishable in the event log, and NULL is the
// honest representation of absent — so `--jira-key ""` clears the column.
func nullString(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

func newProjectShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <project>",
		Short: "Print one project in full",
		Args:  cobra.ExactArgs(1),
		RunE:  runProjectShow,
	}
	addOutputFlag(cmd)
	return cmd
}

func runProjectShow(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	p, err := tx.LoadProject(ctx, args[0])
	if err != nil {
		return notFoundOr(err, args[0])
	}

	if format == outputJSON {
		encoded, err := store.MarshalRecord(p)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}
	return writeRecordDetail(cmd.OutOrStdout(), p)
}

// writeRecordDetail prints every column, one per line. The columns come from
// the record's own metadata, so a new one appears here without being added.
func writeRecordDetail(out io.Writer, r any) error {
	return writeRecordDetailWith(out, r, nil)
}

// writeRecordDetailWith prints a record, followed by rows that are not
// columns of it — an action's blockers, say, which live in another table.
// They share the record's tabwriter so the two halves line up.
func writeRecordDetailWith(out io.Writer, r any, extra [][2]string) error {
	encoded, err := store.MarshalRecord(r)
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		return err
	}

	columns := make([]string, 0, len(object))
	for column := range object {
		columns = append(columns, column)
	}
	sort.Strings(columns)

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, column := range columns {
		fmt.Fprintf(w, "%s\t%s\n", column, detailValue(object[column]))
	}
	for _, row := range extra {
		fmt.Fprintf(w, "%s\t%s\n", row[0], row[1])
	}
	return w.Flush()
}

// detailValue renders one column's JSON for a human: absent as a dash,
// strings unquoted, and arrays or objects left in their JSON form.
func detailValue(raw json.RawMessage) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	switch x := value.(type) {
	case nil:
		return "-"
	case string:
		if x == "" {
			return "-"
		}
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return string(raw)
	}
}

func newProjectSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <project>",
		Short: "Change authored columns on an existing project",
		Long: "Takes the same flags as add, plus --json. Only the columns given are\n" +
			"touched, and one event is logged per column that actually moved — so\n" +
			"setting a value it already has writes nothing.\n\n" +
			"An empty value clears a nullable column: --jira-key \"\" removes it. To\n" +
			"clear a number, use --json '{\"priority\":null}'.\n\n" +
			"Observed columns cannot be set here; they are sync's to write. Use\n" +
			"`project snooze` and `project supersede` for the pairs those verbs\n" +
			"keep consistent.",
		Args: cobra.ExactArgs(1),
		RunE: runProjectSet,
	}
	addProjectFieldFlags(cmd)
	addActorFlag(cmd)
	return cmd
}

func runProjectSet(cmd *cobra.Command, args []string) error {
	if !anyProjectFieldGiven(cmd) {
		return fmt.Errorf("nothing to set: pass a column flag or --%s", flagJSON)
	}

	return updateProject(cmd, args[0], func(_ context.Context, _ *store.Tx, p *store.Project) error {
		if err := applyProjectJSON(cmd, p); err != nil {
			return err
		}
		if err := applyProjectFlags(cmd, p); err != nil {
			return err
		}
		return checkSnoozeConsistency(p)
	})
}

// checkSnoozeConsistency reports the schema's status/snooze_until coupling as
// advice rather than as a CHECK violation.
//
// The database enforces this either way; catching it here is only so the
// message names the verb that moves both.
func checkSnoozeConsistency(p *store.Project) error {
	snoozed := p.Status == store.ProjectSnoozed
	dated := p.SnoozeUntil.Valid

	switch {
	case snoozed && !dated:
		return fmt.Errorf("status %s needs a date: use `project snooze %s --%s <date>`",
			store.ProjectSnoozed, p.ID, flagSnoozeUntil)
	case dated && !snoozed:
		return fmt.Errorf("a snooze date needs status %s: use `project snooze %s --%s <date>`",
			store.ProjectSnoozed, p.ID, flagSnoozeUntil)
	default:
		return nil
	}
}

func newProjectSnoozeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snooze <project>",
		Short: "Defer a project to a real date",
		Long: "--until must be a date or timestamp. \"Next week\" is not one, and that\n" +
			"is the point: a snooze nobody can act on is how work goes quiet.\n\n" +
			"Status and date move together, because the schema couples them.",
		Args: cobra.ExactArgs(1),
		RunE: runProjectSnooze,
	}
	f := cmd.Flags()
	f.String(flagSnoozeUntil, "", "ISO-8601 date or timestamp (required)")
	f.String(flagSnoozeReason, "", "why it is deferred")
	_ = cmd.MarkFlagRequired(flagSnoozeUntil)
	addActorFlag(cmd)
	return cmd
}

func runProjectSnooze(cmd *cobra.Command, args []string) error {
	f := cmd.Flags()

	until, err := f.GetString(flagSnoozeUntil)
	if err != nil {
		return err
	}
	if until, err = validateTimestamp(flagSnoozeUntil, until); err != nil {
		return err
	}
	reason, err := f.GetString(flagSnoozeReason)
	if err != nil {
		return err
	}

	return updateProject(cmd, args[0], func(_ context.Context, _ *store.Tx, p *store.Project) error {
		p.Status = store.ProjectSnoozed
		p.SnoozeUntil = sql.NullString{String: until, Valid: true}
		p.SnoozeReason = reason
		return nil
	})
}

func newProjectWakeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wake <project>",
		Short: "Clear a snooze",
		Args:  cobra.ExactArgs(1),
		RunE:  runProjectWake,
	}
	cmd.Flags().String(flagStatus, store.ProjectActive, "status to wake into")
	addActorFlag(cmd)
	return cmd
}

func runProjectWake(cmd *cobra.Command, args []string) error {
	status, err := cmd.Flags().GetString(flagStatus)
	if err != nil {
		return err
	}
	if status == store.ProjectSnoozed {
		return fmt.Errorf("--%s %s would not wake anything; use `project snooze` to change the date",
			flagStatus, status)
	}

	return updateProject(cmd, args[0], func(_ context.Context, _ *store.Tx, p *store.Project) error {
		if p.Status != store.ProjectSnoozed {
			return fmt.Errorf("%s is %s, not snoozed", p.ID, p.Status)
		}
		p.Status = status
		p.SnoozeUntil = sql.NullString{}
		p.SnoozeReason = ""
		return nil
	})
}

func newProjectSupersedeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "supersede",
		Short: "Record that one project is the same work as another",
		Long: "The superseded project keeps its identifier and stops rendering; the\n" +
			"edge survives so old references still resolve.",
		Args: cobra.NoArgs,
		RunE: runProjectSupersede,
	}
	f := cmd.Flags()
	f.String("from", "", "project being superseded, e.g. SL32 (required)")
	f.String("into", "", "project that replaces it, e.g. SL94 (required)")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("into")
	addActorFlag(cmd)
	return cmd
}

func runProjectSupersede(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()

	from, err := f.GetString("from")
	if err != nil {
		return err
	}
	into, err := f.GetString("into")
	if err != nil {
		return err
	}
	if from == into {
		return fmt.Errorf("%s cannot supersede itself", from)
	}

	return updateProject(cmd, from, func(ctx context.Context, tx *store.Tx, p *store.Project) error {
		// Check the target before writing, so a typo reports the identifier
		// rather than a foreign key violation.
		if _, err := tx.LoadProject(ctx, into); err != nil {
			return notFoundOr(err, into)
		}
		p.Status = store.ProjectSuperseded
		p.SupersededBy = sql.NullString{String: into, Valid: true}
		return nil
	})
}

// updateProject is the read-modify-write every project verb performs: load,
// apply the change to a clone, and let Tx.Update work out what moved.
//
// Nothing is written when the change is a no-op, and the actor rule is
// applied by Update rather than here.
func updateProject(cmd *cobra.Command, id string, change func(context.Context, *store.Tx, *store.Project) error) error {
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

	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	before, err := tx.LoadProject(ctx, id)
	if err != nil {
		return notFoundOr(err, id)
	}

	after := before.Clone()
	if err := change(ctx, tx, after); err != nil {
		return err
	}

	changes, err := tx.Update(ctx, before, after)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if len(changes) == 0 {
		fmt.Fprintf(out, "%s unchanged\n", id)
		return nil
	}
	for _, change := range changes {
		fmt.Fprintf(out, "%s %s\n", id, change)
	}
	return nil
}

// notFoundOr turns a missing row into a message naming the identifier, since
// sql.ErrNoRows on its own says nothing about what was being looked for.
func notFoundOr(err error, id string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("no such item: %s", id)
	}
	return err
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

func newProjectCloseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "close <project>",
		Short: "Close a project, dropping whatever was still open on it",
		Long: "An action exists to advance a project, and a closed project cannot be\n" +
			"advanced — so its open actions are dropped as obsolete rather than left\n" +
			"in the queue pointing at work nobody wants. Anything blocked behind one\n" +
			"of them is released.\n\n" +
			"--status retired is the abandoned case; `todo project supersede` is the\n" +
			"one that records where the work went instead.",
		Args: cobra.ExactArgs(1),
		RunE: runProjectClose,
	}
	cmd.Flags().String(flagStatus, store.ProjectDone,
		store.ProjectDone+" or "+store.ProjectRetired)
	addActorFlag(cmd)
	return cmd
}

func runProjectClose(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	status, err := cmd.Flags().GetString(flagStatus)
	if err != nil {
		return err
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	result, err := st.CloseProject(ctx, actor, args[0], status)
	if err != nil {
		return notFoundOr(err, args[0])
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s %s\n", result.Project.ID, result.Project.Status)
	for _, a := range result.Dropped {
		fmt.Fprintf(out, "  %s dropped: %s was closed\n", a.ID, result.Project.ID)
	}
	for _, a := range result.Freed {
		fmt.Fprintf(out, "  %s is now %s\n", a.ID, a.State)
	}
	return nil
}
