package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

func newVerbCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verb",
		Short: "The action vocabulary",
		Long: "Each verb says how an action using it closes. A predicate verb names a\n" +
			"function in the code; a human verb closes only when someone says so, and\n" +
			"those are the only ones that reach the queue as thinking work.",
	}
	cmd.AddCommand(newVerbListCmd(), newVerbAddCmd(), newVerbSetCmd())
	return cmd
}

func newVerbListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Print the verb vocabulary",
		Args:  cobra.NoArgs,
		RunE:  runVerbList,
	}
	cmd.Flags().Bool("all", false, "include retired verbs")
	addSortFlag(cmd, verbColumns)
	addListingFlags(cmd, verbColumns)
	return cmd
}

// verbColumns is what `verb list` can show.
//
// WAIT is in the default view because it is the one column meant to be tuned,
// and a setting you cannot read is a setting you cannot change with any
// confidence. label and starts_pipeline are not, and are a --fields away.
var verbColumns = columnSet[*store.ActionVerb]{
	blank: &store.ActionVerb{},
	declared: []column[*store.ActionVerb]{
		{name: "predicate_key", header: "PREDICATE"},
		{name: "rank_class", header: "RANK"},
		{name: "requires_pr", header: "PR"},
		{name: "wait_days", header: "WAIT"},
	},
	defaults: []string{
		"verb", "closes", "predicate_key", "rank_class",
		"requires_pr", "wait_days", "active", "description",
	},
	empty: "no verbs",
}

func runVerbList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	all, err := cmd.Flags().GetBool("all")
	if err != nil {
		return err
	}
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	_, sort, err := sortFrom(cmd, verbColumns)
	if err != nil {
		return err
	}
	// Into the query rather than over the rows: whatever SQLite can take, it
	// takes, and runListing runs what is left. See #201.
	cel, err := filterFrom(cmd, &store.ActionVerb{})
	if err != nil {
		return err
	}
	var pushed store.SQLWhere
	pushed.Where, pushed.WhereArgs = cel.SQL()
	cel.WithLoader(cmd.Context(), storeLoader{st: st})

	verbs, err := st.ListVerbs(ctx, !all, sort, pushed)
	if err != nil {
		return err
	}

	return runListing(cmd, verbColumns, verbs, renderContext{})
}

const (
	flagWaitDays  = "wait-days"
	flagRankClass = "rank-class"
)

func newVerbSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <verb>",
		Short: "Change a verb's settings",
		Long: "Settings, not definitions. How long a wait is reasonable and which rank\n" +
			"class a verb sorts in are judgements about one person's queue, and\n" +
			"tuning a number that is meant to be tuned should not mean a migration\n" +
			"and a rebuild.\n\n" +
			"What a verb *means* is not editable here. `closes` and the predicate it\n" +
			"names are checked against the build when the database opens, so a verb\n" +
			"naming a predicate this binary lacks is refused at startup. Letting\n" +
			"those be edited would turn that check into a failure at closing time,\n" +
			"which is the worst moment to discover it.\n\n" +
			"--active=false withdraws a verb. Rows are never deleted: closed actions\n" +
			"and log entries reference retired verbs, and dropping one would take\n" +
			"their meaning with it. A retired verb keeps working everywhere it is\n" +
			"already used and stops being offered for anything new.\n\n" +
			"Verb rows are authored, so a change lands in the log like any other.",
		Args: cobra.ExactArgs(1),
		RunE: runVerbSet,
	}
	f := cmd.Flags()
	f.String(flagWaitDays, "",
		"calendar days of waiting that are reasonable before it is worth noticing; empty means never")
	f.String(flagRankClass, "", "click, decide, session or wait")
	f.Bool(flagActive, true, "false withdraws the verb, leaving what already uses it alone")
	addActorFlag(cmd)
	return cmd
}

func runVerbSet(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	f := cmd.Flags()

	if !f.Changed(flagWaitDays) && !f.Changed(flagRankClass) && !f.Changed(flagActive) {
		return fmt.Errorf("nothing to set: pass --%s, --%s or --%s",
			flagWaitDays, flagRankClass, flagActive)
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

	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	before, err := tx.LoadVerb(ctx, args[0])
	if err != nil {
		return notFoundOr(err, args[0])
	}
	after := before.Clone()

	if f.Changed(flagWaitDays) {
		raw, err := f.GetString(flagWaitDays)
		if err != nil {
			return err
		}
		if after.WaitDays, err = waitDaysFrom(raw); err != nil {
			return err
		}
	}
	if f.Changed(flagRankClass) {
		raw, err := f.GetString(flagRankClass)
		if err != nil {
			return err
		}
		if err := store.ValidateRankClass(raw); err != nil {
			return err
		}
		after.RankClass = raw
	}
	if f.Changed(flagActive) {
		active, err := f.GetBool(flagActive)
		if err != nil {
			return err
		}
		after.Active = active
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
		fmt.Fprintf(out, "%s unchanged\n", before.Verb)
		return nil
	}
	for _, change := range changes {
		fmt.Fprintf(out, "%s %s\n", before.Verb, change)
	}
	return nil
}

// waitDaysFrom reads --wait-days. Empty clears it, which is how a verb is told
// it never times out: work of your own cannot be overdue, only undone.
func waitDaysFrom(raw string) (sql.NullInt64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return sql.NullInt64{}, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 0 {
		return sql.NullInt64{}, fmt.Errorf(
			"--%s %q is not a whole number of days; pass a number, or nothing to never time out",
			flagWaitDays, raw)
	}
	return sql.NullInt64{Int64: int64(days), Valid: true}, nil
}

// Flags for `verb add`. The two that say what a verb *means* live only here:
// `verb set` refuses them deliberately, since the startup check reads them and
// editing one afterwards would turn "this build lacks that predicate" from a
// refusal at startup into a failure at closing time.
const (
	flagCloses         = "closes"
	flagPredicate      = "predicate"
	flagRequiresPR     = "requires-pr"
	flagRequiresRef    = "requires-ref"
	flagRequiresOwner  = "requires-owner"
	flagRequiresIssue  = "requires-issue"
	flagStartsPipeline = "starts-pipeline"
)

// newVerbAddCmd is the gap #241 found: the vocabulary is a table with no
// command that writes rows to it.
//
// The same argument #132 made about pipelines. A verb could only be added by
// writing SQL by hand, which also meant it never reached the event log — and
// the log's whole claim is that it is derived from the data rather than
// authored beside it.
func newVerbAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <verb>",
		Short: "Add a verb to the vocabulary",
		Long: "The vocabulary is data so it can grow without a deploy. The limit is\n" +
			"sharp, and this is where it is enforced: a verb stores a predicate's\n" +
			"name, never its expression, so --predicate must name one this build\n" +
			"registers. A verb naming a predicate that does not exist makes the\n" +
			"database refuse to open — loudly, at startup, for every command — so\n" +
			"the check belongs at the one place a verb is written.\n\n" +
			"--closes human is the other kind: an action nothing can close on your\n" +
			"behalf, which is what reaches the queue as thinking work.\n\n" +
			"The requires-* flags say what an action needs before it can close.\n" +
			"They are separate rather than one \"needs a subject\" because the\n" +
			"remedies differ — `action link-pr` against `action wait-ref` against\n" +
			"`action wait-issue` — and an error naming the wrong one sends somebody\n" +
			"the wrong way.\n\n" +
			"Rows are never deleted: closed actions and log entries reference\n" +
			"retired verbs. `roz verb set --active=false` withdraws one.",
		Args: cobra.ExactArgs(1),
		RunE: runVerbAdd,
	}
	f := cmd.Flags()
	f.String(flagLabel, "", "how it reads on the page; defaults to the verb")
	f.String(flagCloses, store.ClosesHuman,
		"human, or predicate — how an action using it ends")
	f.String(flagPredicate, "",
		"the predicate it closes on, required with --closes predicate; see the error for the registered set")
	f.String(flagRankClass, "", "click, decide, session or wait (required)")
	f.String(flagWaitDays, "",
		"days of waiting that is reasonable; empty means it never times out")
	f.String(flagDescription, "", "what the verb is for, one or two sentences")
	f.Bool(flagRequiresPR, false, "an action needs a subject pull request to close")
	f.Bool(flagRequiresRef, false, "an action needs a ref wait to close")
	f.Bool(flagRequiresOwner, false, "an action needs an owner to close")
	f.Bool(flagRequiresIssue, false, "an action needs a tracker issue to close")
	f.Bool(flagStartsPipeline, false, "closing an action with it instantiates the pipeline")
	_ = cmd.MarkFlagRequired(flagRankClass)
	addActorFlag(cmd)
	return cmd
}

func runVerbAdd(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	f := cmd.Flags()

	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	v := &store.ActionVerb{Verb: args[0], Active: true}

	for _, s := range []struct {
		flag string
		into *string
	}{
		{flagLabel, &v.Label},
		{flagCloses, &v.Closes},
		{flagRankClass, &v.RankClass},
		{flagDescription, &v.Description},
	} {
		if *s.into, err = f.GetString(s.flag); err != nil {
			return err
		}
	}
	for _, b := range []struct {
		flag string
		into *bool
	}{
		{flagRequiresPR, &v.RequiresPR},
		{flagRequiresRef, &v.RequiresRef},
		{flagRequiresOwner, &v.RequiresOwner},
		{flagRequiresIssue, &v.RequiresIssue},
		{flagStartsPipeline, &v.StartsPipeline},
	} {
		if *b.into, err = f.GetBool(b.flag); err != nil {
			return err
		}
	}
	// A label nobody gave is the verb itself, which is what every row without
	// a friendlier reading already looks like.
	if v.Label == "" {
		v.Label = v.Verb
	}
	predicate, err := f.GetString(flagPredicate)
	if err != nil {
		return err
	}
	v.PredicateKey = nullString(predicate)
	rawDays, err := f.GetString(flagWaitDays)
	if err != nil {
		return err
	}
	if v.WaitDays, err = waitDaysFrom(rawDays); err != nil {
		return err
	}
	if err := store.ValidateVerbDefinition(v); err != nil {
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

	// Checked by name rather than left to the primary key, so the message says
	// what happened — and so that re-adding a retired verb is refused with the
	// remedy rather than with a constraint.
	if existing, err := tx.LoadVerb(ctx, v.Verb); err == nil {
		if !existing.Active {
			return fmt.Errorf("%s already exists and is retired; "+
				"`roz verb set %s --active` brings it back", v.Verb, v.Verb)
		}
		return fmt.Errorf("%s is already in the vocabulary", v.Verb)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	if err := tx.Insert(ctx, v); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "%s: %s, %s\n", v.Verb, v.Closes, v.RankClass)
	return nil
}
