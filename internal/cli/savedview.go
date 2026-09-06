package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/filter"
	"github.com/scottlaird/roz/internal/store"
)

const (
	flagEntity = "entity"
	flagView   = "view"
)

// listings are the entities a view can be of, and what each one's columns are.
//
// Derived from the command tree rather than written out again, the way the MCP
// tools are: a listing is something with a column set, and the column set is
// what a view has to be checked against. A new listing appears here by
// existing, not by being remembered.
type listing struct {
	// validate checks a view's filter, sort and fields against this listing's
	// own vocabulary, and says what is wrong in the listing's own words.
	validate func(v *store.View) error
	// names is the vocabulary, for an error that says what was available.
	names []string
}

// checkView is the whole point of validating at save time: a view is written
// once and read for months, so the moment to reject one is while somebody is
// still looking at it.
func checkView(known map[string]listing, v *store.View) error {
	entity, ok := known[v.Entity]
	if !ok {
		offered := make([]string, 0, len(known))
		for name := range known {
			offered = append(offered, name)
		}
		sort.Strings(offered)
		return fmt.Errorf("%q is not a listing a view can be of: use %s",
			v.Entity, strings.Join(offered, ", "))
	}
	return entity.validate(v)
}

// viewOf builds the validator for one listing's column set.
func viewOf[T any](set columnSet[T]) listing {
	return listing{
		names: set.names(),
		validate: func(v *store.View) error {
			if v.Fields != "" {
				if _, err := set.selected(splitList(v.Fields), nil, renderContext{}); err != nil {
					return err
				}
			}
			if v.Sort != "" {
				if err := checkViewSort(set, v.Sort); err != nil {
					return err
				}
			}
			if v.Filter != "" {
				if _, err := filter.Compile(set.recordOf(set.blank), v.Filter); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// checkViewSort accepts what --sort accepts: a ranking, or a list of columns.
func checkViewSort[T any](set columnSet[T], value string) error {
	for _, ranking := range set.rankings {
		if value == ranking {
			return nil
		}
	}
	_, err := set.sortKeys(splitList(value))
	return err
}

// splitList reads a comma-separated flag value the way the flags do.
func splitList(value string) []string {
	parts := strings.Split(value, ",")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		if name := strings.TrimSpace(part); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func newViewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "view",
		Short: "A listing you can name and come back to",
		Long: "Which listing, which columns, which order, which filter — all of it\n" +
			"exists as flags already. A view adds the name, so a question worth\n" +
			"asking twice is not retyped, and the interesting ones can be long\n" +
			"without being unusable.\n\n" +
			"  roz view add stalled --entity project \\\n" +
			"      --filter 'status != \"done\" && snooze_until == null &&\n" +
			"                !actions.exists(a, a.closed_at == null)' \\\n" +
			"      --description 'Live work with nothing open against it'\n\n" +
			"  roz project list --view stalled\n\n" +
			"Adding one needs no code: a filter is an expression over the columns a\n" +
			"listing already declares, so a new question is a row rather than a\n" +
			"flag, a query and a release.\n\n" +
			"A view is checked against its listing when it is saved. A view is\n" +
			"written once and read for months, so the moment to reject one is\n" +
			"while somebody is still looking at it.",
	}
	cmd.AddCommand(newViewAddCmd(), newViewListCmd(), newViewShowCmd(), newViewDropCmd())
	return cmd
}

func newViewAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Save a listing under a name, or change one",
		Long: "Re-saving replaces the whole view: it is short, and \"the view is now\n" +
			"this\" is the only edit anybody makes to one. What it was is in the log.\n\n" +
			"A name is an argument, so it is lower case and has no spaces — one\n" +
			"needing quotes is one nobody types twice.",
		Args: cobra.ExactArgs(1),
		RunE: runViewAdd,
	}
	f := cmd.Flags()
	f.String(flagEntity, "", "which listing it is of: project, action, pr, … (required)")
	f.String(flagFilter, "", "a CEL expression over that listing's columns")
	f.String(sortFlag, "", "how to order it, as `roz <entity> list --sort` takes it")
	f.String(flagFields, "", "which columns to show, comma-separated")
	f.String("description", "", "what it is for, in a sentence")
	_ = cmd.MarkFlagRequired(flagEntity)
	addActorFlag(cmd)
	return cmd
}

func runViewAdd(cmd *cobra.Command, args []string) error {
	f := cmd.Flags()

	v := &store.View{Name: args[0]}
	for _, field := range []struct {
		flag string
		into *string
	}{
		{flagEntity, &v.Entity},
		{flagFilter, &v.Filter},
		{sortFlag, &v.Sort},
		{flagFields, &v.Fields},
		{"description", &v.Description},
	} {
		value, err := f.GetString(field.flag)
		if err != nil {
			return err
		}
		*field.into = strings.TrimSpace(value)
	}

	if err := store.ValidateViewName(v.Name); err != nil {
		return err
	}
	if err := checkView(listings(), v); err != nil {
		return fmt.Errorf("view %q: %w", v.Name, err)
	}

	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	changes, err := st.SaveView(cmd.Context(), actor, v)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if len(changes) == 0 {
		fmt.Fprintf(out, "%s: %s\n", v.Name, describeView(v))
		return nil
	}
	for _, change := range changes {
		fmt.Fprintf(out, "%s %s\n", v.Name, change)
	}
	return nil
}

// describeView says what a view does, for the line that confirms it.
func describeView(v *store.View) string {
	parts := []string{v.Entity}
	if v.Filter != "" {
		parts = append(parts, "where "+v.Filter)
	}
	if v.Sort != "" {
		parts = append(parts, "by "+v.Sort)
	}
	if v.Fields != "" {
		parts = append(parts, "showing "+v.Fields)
	}
	return strings.Join(parts, ", ")
}

func newViewListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Every saved view",
		Args:  cobra.NoArgs,
		RunE:  runViewList,
	}
	cmd.Flags().String(flagEntity, "", "only the views of one listing")
	addSortFlag(cmd, viewColumns)
	addListingFlags(cmd, viewColumns)
	return cmd
}

// viewColumns is what `view list` can show. The filter is in the default view
// because it is the part worth reading: a name is short by design.
var viewColumns = columnSet[*store.View]{
	blank:    &store.View{},
	defaults: []string{"name", "entity", "filter", "sort", "fields", "description"},
	empty:    "no views",
}

func runViewList(cmd *cobra.Command, _ []string) error {
	entity, err := cmd.Flags().GetString(flagEntity)
	if err != nil {
		return err
	}
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	views, err := st.Views(cmd.Context(), entity)
	if err != nil {
		return err
	}
	return runListing(cmd, viewColumns, views, renderContext{})
}

func newViewShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "One view in full, and whether it still works",
		Long: "A view is checked when it is saved, and the columns underneath it can\n" +
			"move afterwards. This re-checks it against the listing as it is now,\n" +
			"so a view broken by a migration says so instead of quietly returning\n" +
			"nothing.",
		Args: cobra.ExactArgs(1),
		RunE: runViewShow,
	}
	addOutputFlag(cmd)
	return cmd
}

func runViewShow(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

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

	v, err := tx.LoadView(ctx, args[0])
	if err != nil {
		return notFoundOr(err, args[0])
	}
	tx.Rollback()

	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	if format == outputJSON {
		encoded, err := store.MarshalRecord(v)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s  %s\n", v.Name, v.Entity)
	if v.Description != "" {
		fmt.Fprintf(out, "%s\n", v.Description)
	}
	for _, part := range []struct{ label, value string }{
		{"filter", v.Filter},
		{"sort", v.Sort},
		{"fields", v.Fields},
	} {
		if part.value != "" {
			fmt.Fprintf(out, "%-7s %s\n", part.label, part.value)
		}
	}

	// The check `add` did, run again against the listing as it is now: the
	// columns underneath a view can move after it was written, and a view
	// broken by a migration should say so rather than quietly return nothing.
	if err := checkView(listings(), v); err != nil {
		fmt.Fprintf(out, "\nbroken: %v\n", err)
		return nil
	}
	fmt.Fprintf(out, "\nroz %s list --view %s\n", v.Entity, v.Name)
	return nil
}

func newViewDropCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "drop <name>",
		Short: "Forget a view",
		Long:  "The listing it was of is untouched.",
		Args:  cobra.ExactArgs(1),
		RunE:  runViewDrop,
	}
	addActorFlag(cmd)
	return cmd
}

func runViewDrop(cmd *cobra.Command, args []string) error {
	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.DropView(cmd.Context(), actor, args[0]); err != nil {
		return notFoundOr(err, args[0])
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s dropped\n", args[0])
	return nil
}

// listings is which entities a view can be of, with the column set each one
// is checked against.
//
// One place, and it is the same column sets the listings themselves use — so a
// view cannot be validated against a vocabulary the listing does not have.
func listings() map[string]listing {
	return map[string]listing{
		"project":  viewOf(projectColumns),
		"action":   viewOf(actionColumns),
		"pr":       viewOf(prColumns),
		"issue":    viewOf(issueColumns),
		"ref":      viewOf(refColumns),
		"repo":     viewOf(repoColumns),
		"calendar": viewOf(calendarColumns),
		"verb":     viewOf(verbColumns),
		"pipeline": viewOf(pipelineColumns),
		"owner":    viewOf(ownerColumns),
		"page":     viewOf(pageColumns),
		// The log, which is a filter source without being a listing: `roz
		// watch` takes --filter and --view, and has no --fields or --sort
		// because a tail is one line per event in the order they happened.
		// The registry is what makes a saved view of it checkable.
		entityEvent: viewOf(eventColumns),
	}
}

// entityEvent is the log's name as a view entity. A constant because `watch`
// is a top-level command, so unlike a listing it cannot read its entity off
// the command path.
const entityEvent = "event"

// addViewFlag registers --view on a listing.
func addViewFlag(cmd *cobra.Command) {
	cmd.Flags().String(flagView, "",
		"a saved view to start from; see `roz view list`. Explicit flags win over it")
}

// applyView reads --view and fills in whatever the command did not say.
//
// The view supplies defaults rather than overriding: `roz project list --view
// stalled --fields id,title` is somebody wanting that view shown differently,
// and a saved answer that ignored what they just typed would be a saved answer
// nobody trusts.
//
// The view has to be of this listing. A project view applied to `pr list`
// would name columns that do not exist there, and the error for that should
// say what went wrong rather than "no such column".
func applyView(cmd *cobra.Command, entity string) error {
	name, err := cmd.Flags().GetString(flagView)
	if err != nil || name == "" {
		return err
	}

	// Its own handle, opened and closed here: this runs before the command
	// opens the one it will use, and leaving a closed store where the listing
	// expects to find an open one would be a subtle way to break relation
	// loading.
	dbPath, err := dbPathFrom(cmd)
	if err != nil {
		return err
	}
	st, err := store.OpenStore(cmd.Context(), dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	tx, err := st.Begin(cmd.Context(), store.ActorHuman)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	v, err := tx.LoadView(cmd.Context(), name)
	if err != nil {
		return notFoundOr(err, name)
	}
	tx.Rollback()

	if v.Entity != entity {
		return fmt.Errorf("view %q is of %s, and this is %s: see `roz %s list --view %s`",
			v.Name, v.Entity, entity, v.Entity, v.Name)
	}

	for _, part := range []struct{ flag, value string }{
		{flagFilter, v.Filter},
		{sortFlag, v.Sort},
		{flagFields, v.Fields},
	} {
		if part.value == "" || cmd.Flags().Changed(part.flag) {
			continue
		}
		if err := cmd.Flags().Set(part.flag, part.value); err != nil {
			return fmt.Errorf("view %q sets --%s, which this listing does not take: %w",
				v.Name, part.flag, err)
		}
	}
	return nil
}

// entityOf names the listing a command is of, which is the same name a view
// stores: the command's parent.
func entityOf(cmd *cobra.Command) string {
	if cmd.Parent() == nil {
		return ""
	}
	return cmd.Parent().Name()
}
