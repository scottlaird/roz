package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Bodies of work, allocated a ROZ identifier",
	}
	cmd.AddCommand(
		newProjectAddCmd(),
		newProjectShowCmd(),
		newProjectSetCmd(),
		newProjectSnoozeCmd(),
		newProjectWakeCmd(),
		newProjectSupersedeCmd(),
		newProjectCloseCmd(),
		newProjectBlockCmd(),
		newProjectUnblockCmd(),
		newProjectListCmd(),
		newProjectLinkIssueCmd(),
		newProjectUnlinkIssueCmd(),
	)
	return cmd
}

// Flag names for the authored columns settable at creation. superseded_by is
// not among them: use `project supersede`, which records both ends.
const (
	// flagJiraKeyOld is --jira-key, kept working while --issue takes over.
	flagJiraKeyOld   = "jira-key"
	flagTitle        = "title"
	flagSummary      = "summary"
	flagStatus       = "status"
	flagPriority     = "priority"
	flagEffort       = "effort"
	flagSnoozeUntil  = "snooze-until"
	flagSnoozeReason = "snooze-reason"
	flagDesignRef    = "design-ref"
	flagIssueKey     = "issue"
	flagJSON         = "json"
	flagParent       = "parent"
	flagTree         = "tree"
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
	flagSnoozeUntil, flagSnoozeReason, flagDesignRef, flagParent,
}

func addProjectFieldFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String(flagTitle, "", "what the project is")
	f.String(flagSummary, "", "how it relates to other items; not a design note")
	f.String(flagStatus, "", "active, blocked, snoozed, done, retired or superseded")
	f.Int(flagPriority, 0, "1 to 9")
	f.String(flagEffort, "", "minutes, hours, session, days or weeks")
	f.String(flagSnoozeUntil, "", "ISO-8601 date or timestamp; requires status snoozed")
	f.String(flagSnoozeReason, "", "why it is deferred")
	f.StringArray(flagDesignRef, nil, "path to a design note; repeatable")
	f.StringArray(flagIssueKey, nil, "tracker issue to track, e.g. CDSS-1744; repeatable")
	f.String(flagParent, "",
		"the project this is part of, e.g. SL7; \"\" clears it. Display only: a parent does not block or close a child")
	f.StringArray(flagJiraKeyOld, nil, "issue to track, e.g. CDSS-1744; repeatable")
	_ = f.MarkDeprecated(flagJiraKeyOld, "use --issue, with --tracker if it is not Jira")
	addTrackerFlag(cmd)
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
	st, err := openStore(cmd)
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

	// Before the insert, so a mistyped parent is refused by name rather than
	// as a foreign key. A brand-new project cannot make a cycle: nothing
	// points at it yet.
	parent, err := cmd.Flags().GetString(flagParent)
	if err != nil {
		return err
	}
	if parent != "" {
		if _, err := tx.LoadProject(ctx, parent); err != nil {
			return notFoundOr(err, parent)
		}
		p.ParentID = sql.NullString{String: parent, Valid: true}
	}

	if err := tx.Insert(ctx, p); err != nil {
		return err
	}
	// Linking happens here rather than in applyProjectFlags because it is a
	// row in another table, not a column on this one.
	keys, err := issueKeysFrom(cmd)
	if err != nil {
		return err
	}
	tracker, err := cmd.Flags().GetString(flagTracker)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := tx.LinkProjectIssue(ctx, p.ID, tracker, key); err != nil {
			return err
		}
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

	p, err := tx.LoadProject(ctx, args[0])
	if err != nil {
		return notFoundOr(err, args[0])
	}

	return showRecord(cmd, ctx, tx, p, format)
}

// writeRecordDetail prints every column, one per line. The columns come from
// the record's own metadata, so a new one appears here without being added.
//
// For a record with relations, prefer showRecord: this sees only columns.
func writeRecordDetail(out io.Writer, r any) error {
	encoded, err := store.MarshalRecord(r)
	if err != nil {
		return err
	}
	return writeDetail(out, encoded)
}

// writeDetail prints an already-encoded record, one key per line.
//
// It reads the JSON rather than the struct, which is what lets a relation
// print beside the columns without a second mechanism: whatever the encoder
// put in, this lays out. The keys are sorted, so a relation appears wherever
// its name falls rather than tacked on the end.
func writeDetail(out io.Writer, encoded []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		return err
	}

	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, key := range keys {
		fmt.Fprintf(w, "%s\t%s\n", key, detailValue(object[key]))
	}
	return w.Flush()
}

// detailValue renders one column's JSON for a detail view, where an absent
// value is a dash. A listing renders its cells the same way — see renderCell,
// which this is now the one-argument case of.
func detailValue(raw json.RawMessage) string { return renderCell(raw, detailStyle) }

// joinStrings renders a JSON array of strings as a comma-separated list,
// reporting false for anything else — an array of objects has no obvious
// one-line form, and inventing one would hide what is in it.
func joinStrings(values []any) (string, bool) {
	parts := make([]string, len(values))
	for i, v := range values {
		s, ok := v.(string)
		if !ok {
			return "", false
		}
		parts[i] = s
	}
	return strings.Join(parts, ", "), true
}

func newProjectSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <project>",
		Short: "Change authored columns on an existing project",
		Long: "Takes the same columns as add, plus --json. Only the columns given are\n" +
			"touched, and one event is logged per column that actually moved — so\n" +
			"setting a value it already has writes nothing.\n\n" +
			"An empty value clears a nullable column: --summary \"\" removes it. To\n" +
			"clear a number, use --json '{\"priority\":null}'.\n\n" +
			"Which issues a project tracks is not among them. That is a link\n" +
			"rather than a column — a project may track several, from more than\n" +
			"one tracker — so `project link-issue` and `project unlink-issue` are\n" +
			"what change it, one at a time and each saying which way it went.\n\n" +
			"Observed columns cannot be set here; they are sync's to write. Use\n" +
			"`project snooze` and `project supersede` for the pairs those verbs\n" +
			"keep consistent.",
		Args: cobra.ExactArgs(1),
		RunE: runProjectSet,
	}
	addProjectFieldFlags(cmd)
	// The same flag means something different here. On `add` it links the
	// issues a new project tracks; on `set` there is nothing for it to do, so
	// its replacement is a command rather than another flag.
	_ = cmd.Flags().MarkDeprecated(flagJiraKeyOld, "use `roz project link-issue`")
	addActorFlag(cmd)
	return cmd
}

// issueFlagsOnSet refuses the two flags that look settable and are not,
// naming the command that does the job.
//
// Rejecting a flag and being given no flags at all are different failures, and
// "nothing to set" for an invocation that plainly asked for something is the
// answer to the wrong question — the flag was the problem, not its absence.
//
// Refused rather than implemented, because a project may track several issues
// from more than one tracker. `set` assigns columns, and a flag that looked
// like one would have to decide silently whether it added or replaced. Replace
// is the more dangerous default and add is not what `set` means anywhere else.
func issueFlagsOnSet(cmd *cobra.Command, project string) error {
	f := cmd.Flags()
	for _, name := range []string{flagIssueKey, flagJiraKeyOld} {
		if !f.Changed(name) {
			continue
		}
		keys, err := f.GetStringArray(name)
		if err != nil {
			return err
		}
		key := "CDSS-1744"
		if len(keys) > 0 && keys[0] != "" {
			key = keys[0]
		}
		return fmt.Errorf(
			"--%s does not set a column: which issues a project tracks is a link, "+
				"since one project may track several. Use `roz project link-issue "+
				"--project %s --issue %s` (and --tracker if it is not Jira), or "+
				"`unlink-issue` to remove one",
			name, project, key)
	}
	return nil
}

func runProjectSet(cmd *cobra.Command, args []string) error {
	if err := issueFlagsOnSet(cmd, args[0]); err != nil {
		return err
	}
	if !anyProjectFieldGiven(cmd) {
		return fmt.Errorf("nothing to set: pass a column flag or --%s", flagJSON)
	}

	return updateProject(cmd, args[0], func(ctx context.Context, tx *store.Tx, p *store.Project) error {
		if err := applyProjectJSON(cmd, p); err != nil {
			return err
		}
		if err := applyProjectFlags(cmd, p); err != nil {
			return err
		}
		// Through the store rather than as a plain column, because a parent is
		// the one field here whose validity depends on the rest of the table:
		// a chain that comes back round is only visible by walking it.
		if cmd.Flags().Changed(flagParent) {
			parent, err := cmd.Flags().GetString(flagParent)
			if err != nil {
				return err
			}
			if err := checkParent(ctx, tx, p, parent); err != nil {
				return err
			}
			p.ParentID = sql.NullString{String: parent, Valid: parent != ""}
		}
		return checkSnoozeConsistency(p)
	})
}

// checkParent refuses a parent that does not exist or that would make a
// project its own ancestor.
//
// Read-only: the write is the caller's ordinary update, so a parent change is
// one event beside whatever else was set rather than a second write of its
// own.
func checkParent(ctx context.Context, tx *store.Tx, p *store.Project, parent string) error {
	if parent == "" {
		return nil
	}
	if _, err := tx.LoadProject(ctx, parent); err != nil {
		return notFoundOr(err, parent)
	}
	return tx.CheckParent(ctx, p, parent)
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
			"is the point: a snooze nobody can act on is how work goes quiet.\n" +
			"The date arriving wakes it on the next sync.\n\n" +
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
	f.String("from", "", "project being superseded, e.g. ROZ32 (required)")
	f.String("into", "", "project that replaces it, e.g. ROZ94 (required)")
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

// updateProject is the read-modify-write every project verb performs.
func updateProject(cmd *cobra.Command, id string, change func(context.Context, *store.Tx, *store.Project) error) error {
	_, err := updateRecord(cmd, id, (*store.Tx).LoadProject, change)
	return err
}

func newProjectListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "The projects table",
		Long: "Creation order by default; --sort priority puts the highest first,\n" +
			"with anything unprioritised last.",
		Args: cobra.NoArgs,
		RunE: runProjectList,
	}
	f := cmd.Flags()
	f.Bool("orphaned", false, "no open action and no snooze — how live work goes quiet")
	f.Bool("expired", false, "snoozed with a date that has passed")
	f.String("status", "", "filter to one status")
	f.Bool(flagTree, false,
		"draw the hierarchy, indenting each project under the one it is part of")
	addSortFlag(cmd, projectColumns)
	addListingFlags(cmd, projectColumns)
	return cmd
}

func runProjectList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	query, err := projectFilterFrom(cmd)
	if err != nil {
		return err
	}
	// The second listing to push a CEL filter into its query, and the one
	// where it matters: a filter that reaches a project's actions or children
	// is a correlated subquery here and a query per row otherwise.
	cel, err := filterFrom(cmd, &store.Project{})
	if err != nil {
		return err
	}
	query.Where, query.WhereArgs = cel.Take()
	// Whatever the query could not take runs in Go, and a traversal there
	// needs somewhere to read the far side from.
	cel.WithLoader(ctx, storeLoader{st: st})

	projects, err := st.ListProjects(ctx, query)
	if err != nil {
		return err
	}

	tree, err := cmd.Flags().GetBool(flagTree)
	if err != nil {
		return err
	}
	if !tree {
		// Flat by default: most projects have no parent, and a hierarchy of
		// one level is a list with extra ceremony.
		return runListing(cmd, projectColumns, flatNodes(projects), renderContext{})
	}

	// A filtered list can name a parent it does not contain — showing only
	// open projects, under a closed one. Those are pulled back in so the tree
	// has no gaps, and marked, since they are context rather than work.
	connected, err := st.WithAncestors(ctx, projects)
	if err != nil {
		return err
	}
	return runListing(cmd, projectColumns, store.Tree(connected), renderContext{})
}

// projectColumns is what `project list` can show.
//
// Its rows are tree nodes rather than projects: a node is a project and how
// deep it sits, and the depth is what the title is indented by. So the set
// says where the record is, and everything but the title reads off it as
// usual.
//
// The title carries the indentation rather than the identifier, because
// indenting that would make the column unreadable down the page and unusable
// to copy out of, which is most of what anybody does with it.
var projectColumns = columnSet[store.TreeNode]{
	blank:  store.TreeNode{Project: &store.Project{}},
	record: func(n store.TreeNode) any { return n.Project },
	declared: []column[store.TreeNode]{
		{name: "priority", header: "PRI"},
		{
			name: "snooze_until", header: "SNOOZED UNTIL",
			render: func(n store.TreeNode, ctx renderContext) string {
				return snoozeCell(n.Project.SnoozeUntil, ctx.now)
			},
		},
		{
			name: "title",
			render: func(n store.TreeNode, _ renderContext) string {
				title := strings.Repeat("  ", n.Depth) + n.Project.Title
				if n.Context {
					// Shown to hold its children up, not because it is live.
					title += "  (closed)"
				}
				return title
			},
		},
	},
	defaults: []string{"id", "status", "priority", "effort", "snooze_until", "title"},
	rankings: queueRankings,
	empty:    "no projects",
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
	order, sort, err := sortFrom(cmd, projectColumns)
	if err != nil {
		return store.ProjectFilter{}, err
	}
	return store.ProjectFilter{
		Status: status, Expired: expired, Orphaned: orphaned, Order: order, Sort: sort,
	}, nil
}

// flatNodes is the listing as it has always been: no depth, no context rows.
func flatNodes(projects []*store.Project) []store.TreeNode {
	nodes := make([]store.TreeNode, len(projects))
	for i, p := range projects {
		nodes[i] = store.TreeNode{Project: p}
	}
	return nodes
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
			"--status retired is the abandoned case; `roz project supersede` is the\n" +
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

	st, err := openStore(cmd)
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
	for _, p := range result.Unblocked {
		fmt.Fprintf(out, "  %s is now %s\n", p.ID, p.Status)
	}
	return nil
}

// showRecord prints one record in either format, relations included.
//
// One function rather than one per entity, because `show` printing an
// action's blockers while `show -o json` silently omitted them is the exact
// shape of bug the relation declarations exist to stop. Both formats now read
// the same list.
func showRecord(cmd *cobra.Command, ctx context.Context, tx *store.Tx, r store.Record, format string) error {
	return showRecordWith(cmd, ctx, tx, r, format, nil)
}

// showRecordWith is showRecord with answers no column holds.
//
// Both formats get them, for the reason showRecord is one function: a detail
// view that says something `-o json` does not is how a reader and an agent end
// up with different pictures of the same row. Keys are added rather than
// replaced, so a computed answer can never quietly stand in for a column.
func showRecordWith(cmd *cobra.Command, ctx context.Context, tx *store.Tx,
	r store.Record, format string, extra map[string]string) error {

	encoded, err := tx.MarshalRecord(ctx, r)
	if err != nil {
		return err
	}
	if len(extra) > 0 {
		if encoded, err = withKeys(encoded, extra); err != nil {
			return err
		}
	}
	if format == outputJSON {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}
	return writeDetail(cmd.OutOrStdout(), encoded)
}

// withKeys merges computed values into an encoded record, refusing to shadow
// a column: a key that is already there is a name collision, and silently
// winning it would make the record lie about itself.
func withKeys(encoded []byte, extra map[string]string) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, err
	}
	for key, value := range extra {
		if _, taken := object[key]; taken {
			return nil, fmt.Errorf("%q is already a column; a computed value may not shadow it", key)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		object[key] = raw
	}
	return json.Marshal(object)
}

// newProjectBlockCmd mirrors `action add-blocker`, because one project
// waiting on another is the same relationship as one action waiting on
// another. Same flags, same direction, same reading.
func newProjectBlockCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "block",
		Short: "Record that one project must finish before another can start",
		Long: "The blocked project moves to blocked, and returns to active when its\n" +
			"last open blocker closes. Until this existed, `blocked` was a status\n" +
			"with nothing recording what it was blocked on, so the dependency lived\n" +
			"in a summary and nothing ever cleared it.\n\n" +
			"A blocked project still appears on the page, marked. It is not hidden:\n" +
			"blocked is exactly where work goes quiet.",
		Args: cobra.NoArgs,
		RunE: runProjectBlock,
	}
	f := cmd.Flags()
	f.String(flagFrom, "", "blocked project (required)")
	f.String(flagTo, "", "project that blocks it (required)")
	_ = cmd.MarkFlagRequired(flagFrom)
	_ = cmd.MarkFlagRequired(flagTo)
	addActorFlag(cmd)
	return cmd
}

func runProjectBlock(cmd *cobra.Command, _ []string) error {
	blocked, blocker, err := blockingPair(cmd)
	if err != nil {
		return err
	}

	return withActionTx(cmd, func(ctx context.Context, tx *store.Tx) error {
		blockedProject, blockerProject, err := loadPair(ctx, tx, blocked, blocker)
		if err != nil {
			return err
		}
		if !blockerProject.IsOpen() {
			return fmt.Errorf("%s is already %s and blocks nothing",
				blockerProject.ID, blockerProject.Status)
		}
		if err := tx.BlockProject(ctx, blockerProject, blockedProject); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s is %s, waiting on %s\n",
			blockedProject.ID, blockedProject.Status, blockerProject.ID)
		return nil
	})
}

func newProjectUnblockCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unblock",
		Short: "Remove a dependency between two projects",
		Long: "For when the dependency was wrong rather than satisfied. Saying so\n" +
			"should not require closing something that is not done.\n\n" +
			"The blocked project returns to active if this was its last open\n" +
			"blocker.",
		Args: cobra.NoArgs,
		RunE: runProjectUnblock,
	}
	f := cmd.Flags()
	f.String(flagFrom, "", "blocked project (required)")
	f.String(flagTo, "", "project that was blocking it (required)")
	_ = cmd.MarkFlagRequired(flagFrom)
	_ = cmd.MarkFlagRequired(flagTo)
	addActorFlag(cmd)
	return cmd
}

func runProjectUnblock(cmd *cobra.Command, _ []string) error {
	blocked, blocker, err := blockingPair(cmd)
	if err != nil {
		return err
	}

	return withActionTx(cmd, func(ctx context.Context, tx *store.Tx) error {
		blockedProject, blockerProject, err := loadPair(ctx, tx, blocked, blocker)
		if err != nil {
			return err
		}
		if err := tx.UnblockProject(ctx, blockerProject, blockedProject); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s is %s, no longer waiting on %s\n",
			blockedProject.ID, blockedProject.Status, blockerProject.ID)
		return nil
	})
}

// blockingPair reads --from and --to, which name the blocked one and its
// blocker in that order — the same way `action add-blocker` reads them.
func blockingPair(cmd *cobra.Command) (blocked, blocker string, err error) {
	f := cmd.Flags()
	if blocked, err = f.GetString(flagFrom); err != nil {
		return "", "", err
	}
	blocker, err = f.GetString(flagTo)
	return blocked, blocker, err
}

func loadPair(ctx context.Context, tx *store.Tx, blockedID, blockerID string) (blocked, blocker *store.Project, err error) {
	if blocked, err = tx.LoadProject(ctx, blockedID); err != nil {
		return nil, nil, notFoundOr(err, blockedID)
	}
	if blocker, err = tx.LoadProject(ctx, blockerID); err != nil {
		return nil, nil, notFoundOr(err, blockerID)
	}
	return blocked, blocker, nil
}

// issueKeysFrom reads --issue, accepting the superseded --jira-key too.
func issueKeysFrom(cmd *cobra.Command) ([]string, error) {
	f := cmd.Flags()
	keys, err := f.GetStringArray(flagIssueKey)
	if err != nil {
		return nil, err
	}
	old, err := f.GetStringArray(flagJiraKeyOld)
	if err != nil {
		return nil, err
	}
	return append(keys, old...), nil
}
