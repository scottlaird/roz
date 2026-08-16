package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// What each hand-written filter flag says, written as a filter.
//
// The flags are not implemented this way, and should not be: a tested one-line
// WHERE clause is cheaper than a parse, a type-check, an equivalence probe and
// a conversion that produce the same SQL. What this buys is the other
// direction — somebody who already uses `--orphaned` can read what it means in
// the language they will write saved views in, and discover the vocabulary
// from a flag rather than from documentation.
//
// A claim like this rots the moment either side changes, so
// TestFlagsAndFiltersAgree runs both against the same rows and compares. That
// is the same bargain schema.sql makes with the migrations: content in a file
// is only worth having if something checks it.
type flagMeaning struct {
	// filter is the expression, or empty where the flag takes a value and the
	// expression has to be built from it.
	filter string
	// withValue builds the expression from the flag's value.
	withValue func(value string) string
	// exact says the two select the same rows. Where false, the note says how
	// they differ — a flag that also sorts, or that needs a clock.
	exact bool
	note  string
}

// prFlagFilters is what `pr list`'s flags mean.
var prFlagFilters = map[string]flagMeaning{
	"state":   {withValue: func(v string) string { return fmt.Sprintf("state == %q", v) }, exact: true},
	"because": {withValue: func(v string) string { return fmt.Sprintf("tracked_because == %q", v) }, exact: true},
	"stacked": {filter: "stacked_on != null", exact: true},
	"frozen":  {filter: "frozen == true", exact: true},
	"since": {
		withValue: func(v string) string { return fmt.Sprintf("merged_at >= %q", v) },
		exact:     true,
		note:      "and orders by merged_at, which a filter does not do",
	},
}

// actionFlagFilters is what `action list`'s flags mean.
var actionFlagFilters = map[string]flagMeaning{
	"status":  {withValue: func(v string) string { return fmt.Sprintf("state == %q", v) }, exact: true},
	"verb":    {withValue: func(v string) string { return fmt.Sprintf("verb == %q", v) }, exact: true},
	"project": {withValue: func(v string) string { return fmt.Sprintf("project_id == %q", v) }, exact: true},
	"open":    {filter: "closed_at == null", exact: true},
	"unblocked": {
		note: "the queue, which is a ranking rather than a predicate: it reads the " +
			"verb's rank class and whether the project is blocked, neither of which " +
			"is a column of the action",
	},
	"waiting": {note: "the same machinery as --unblocked, inverted"},
	"expired": {filter: `state == "snoozed" && snooze_until < now`, exact: true},
}

// projectFlagFilters is what `project list`'s flags mean.
var projectFlagFilters = map[string]flagMeaning{
	"status": {withValue: func(v string) string { return fmt.Sprintf("status == %q", v) }, exact: true},
	"orphaned": {
		filter: `status != "done" && status != "retired" && status != "superseded" && ` +
			`snooze_until == null && !actions.exists(a, a.closed_at == null)`,
		exact: true,
	},
	"expired": {filter: `status == "snoozed" && snooze_until < now`, exact: true},
}

// explainFlags prints what the flags somebody actually passed would be as a
// filter.
//
// Only the ones they used: a listing of every flag's translation is a manual,
// and this is meant to answer "what did I just ask for" at the moment they
// asked it.
func explainFlags(cmd *cobra.Command, known map[string]flagMeaning) {
	var lines []string
	for name, meaning := range known {
		flag := cmd.Flags().Lookup(name)
		if flag == nil || !flag.Changed {
			continue
		}
		expression := meaning.filter
		if meaning.withValue != nil {
			expression = meaning.withValue(flag.Value.String())
		}
		lines = append(lines, describeFlag(name, expression, meaning))
	}
	if len(lines) == 0 {
		return
	}
	sort.Strings(lines)
	fmt.Fprintln(cmd.ErrOrStderr(), strings.Join(lines, "\n"))
}

func describeFlag(name, expression string, meaning flagMeaning) string {
	switch {
	case expression == "":
		return fmt.Sprintf("--%s is %s", name, meaning.note)
	case meaning.note != "":
		return fmt.Sprintf("--%s is --filter %s, %s", name, shellQuoted(expression), meaning.note)
	default:
		return fmt.Sprintf("--%s is --filter %s", name, shellQuoted(expression))
	}
}

// shellQuoted wraps an expression the way somebody would have to type it.
//
// Single quotes, because every expression here contains double ones and %q
// would render them escaped — producing a line that reads as an explanation
// and cannot be pasted, which is most of what this is for.
func shellQuoted(expression string) string {
	return "'" + strings.ReplaceAll(expression, "'", `'\''`) + "'"
}
