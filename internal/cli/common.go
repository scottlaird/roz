package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

const actorFlag = "actor"

// addActorFlag registers --actor on a command that writes.
func addActorFlag(cmd *cobra.Command) {
	cmd.Flags().String(actorFlag, string(store.ActorHuman),
		"who is making the change: human, or agent:<name>")
}

// actorFrom reads and validates --actor.
//
// sync: actors are refused here. They are set by `roz sync`, and letting a
// person pass one by hand would turn the authored/observed rule into a
// suggestion — the whole point is that sync cannot overwrite a judgement.
func actorFrom(cmd *cobra.Command) (store.Actor, error) {
	value, err := cmd.Flags().GetString(actorFlag)
	if err != nil {
		return "", err
	}
	switch {
	case value == string(store.ActorHuman):
		return store.ActorHuman, nil
	case strings.HasPrefix(value, "agent:") && len(value) > len("agent:"):
		return store.Actor(value), nil
	case strings.HasPrefix(value, "sync:"):
		return "", fmt.Errorf("--actor %s is not allowed: sync actors are set by `roz sync`, "+
			"because only they may write observed fields", value)
	default:
		return "", fmt.Errorf("--actor %q is not recognised: use human or agent:<name>", value)
	}
}

// timestampLayouts are the forms a date or timestamp flag accepts. A bare
// date is the common case; the full form is what the log itself stores.
var timestampLayouts = []string{
	"2006-01-02",
	"2006-01-02T15:04:05.000Z",
	"2006-01-02T15:04:05Z",
	time.RFC3339,
}

// validateTimestamp checks a flag value is a real date rather than something
// like "next week", which would otherwise compare as text and quietly match
// nothing. The value is returned unchanged, because ISO-8601 compares
// correctly as text.
//
// The sketch is emphatic about this one: a snooze date "must be a real
// timestamp — 'next week' is not one, and that is the point".
func validateTimestamp(flag, value string) (string, error) {
	for _, layout := range timestampLayouts {
		if _, err := time.Parse(layout, value); err == nil {
			return value, nil
		}
	}
	return "", fmt.Errorf("--%s %q is not a date or timestamp: use 2006-01-02 or 2006-01-02T15:04:05Z",
		flag, value)
}

// Output formats for read commands.
const (
	outputFlag  = "output"
	outputTable = "table"
	outputJSON  = "json"
	// outputCSV is for the listing that is going somewhere else. It is a
	// format under -o rather than a --csv flag for the reason json is: one
	// place to ask the question, and room for the next answer.
	//
	// Listings only. A detail view is a column per line and already reads
	// like two columns of CSV, which is not what anybody asking for CSV
	// wants — so `show` does not offer it rather than offering something
	// useless.
	outputCSV = "csv"
)

// addOutputFlag registers -o on a command that reports.
//
// It is --output rather than --json because on write commands --json already
// means the input object; one name cannot mean both. It also leaves room for
// other formats without another boolean flag.
func addOutputFlag(cmd *cobra.Command) {
	cmd.Flags().StringP(outputFlag, "o", outputTable,
		"output format: table or json")
}

func outputFrom(cmd *cobra.Command) (string, error) {
	value, err := cmd.Flags().GetString(outputFlag)
	if err != nil {
		return "", err
	}
	switch value {
	case outputTable, outputJSON:
		return value, nil
	default:
		return "", fmt.Errorf("--output %q is not recognised: use %s or %s",
			value, outputTable, outputJSON)
	}
}

// addListOutputFlag registers -o on a listing, which can also write CSV.
//
// Separate from addOutputFlag so that a format is offered only where it is
// implemented: `show -o csv` would otherwise be accepted and quietly print a
// table, which is worse than refusing it.
func addListOutputFlag(cmd *cobra.Command) {
	cmd.Flags().StringP(outputFlag, "o", outputTable,
		"output format: table, json or csv")
}

// listOutputFrom is outputFrom with CSV allowed.
func listOutputFrom(cmd *cobra.Command) (string, error) {
	value, err := cmd.Flags().GetString(outputFlag)
	if err != nil {
		return "", err
	}
	switch value {
	case outputTable, outputJSON, outputCSV:
		return value, nil
	default:
		return "", fmt.Errorf("--output %q is not recognised: use %s, %s or %s",
			value, outputTable, outputJSON, outputCSV)
	}
}

// dbPathFrom reads --db off the command rather than a package variable.
//
// Cobra keeps a parsed flag's value on the flag, and a subcommand's FlagSet
// includes the persistent flags of its parents, so this reaches the root's
// --db from anywhere in the tree — and reaches *this* tree's copy of it.
func dbPathFrom(cmd *cobra.Command) (string, error) {
	path, err := cmd.Flags().GetString("db")
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", errors.New("no database path: pass --db or set ROZ_DB")
	}
	return path, nil
}

// openStore opens the database named by --db, translating an uninitialised
// database into advice rather than a missing-table error.
func openStore(cmd *cobra.Command) (*store.Store, error) {
	dbPath, err := dbPathFrom(cmd)
	if err != nil {
		return nil, err
	}
	st, err := store.OpenStore(dbPath)
	if err != nil {
		if errors.Is(err, store.ErrNotInitialised) {
			return nil, fmt.Errorf("%w; run `roz init` first", err)
		}
		return nil, err
	}
	return st, nil
}

// The rankings a listing may offer, beyond ordering by its own columns.
//
// They are reserved words in --sort rather than a second flag: they are the
// orderings somebody actually types, and splitting them off would mean
// learning which flag a given ordering lives behind. What they are not is
// columns — priority is a CTE, two joins and an expression over three tables,
// and staleness is a computed date — so they cannot be keys in a list of them.
const (
	sortFlag      = "sort"
	sortCreated   = "created"
	sortPriority  = "priority"
	sortStaleness = "staleness"
	// descendingPrefix reverses one key: --sort -merged_at is newest first.
	descendingPrefix = "-"
)

// queueRankings are what `action list` and `project list` accept besides
// their columns.
var queueRankings = []string{sortCreated, sortPriority, sortStaleness}

// addSortFlag registers --sort on a listing that declares its columns.
func addSortFlag[T any](cmd *cobra.Command, set columnSet[T]) {
	usage := "order by columns, comma-separated, - for descending: title,-created_at"
	if len(set.rankings) > 0 {
		usage += "; or one of " + strings.Join(set.rankings, ", ")
	}
	cmd.Flags().String(sortFlag, "", usage)
}

// sortFrom resolves --sort into either one of the listing's rankings or an
// ordering by its columns.
//
// Empty and empty is "however this listing has always come back", which is
// what leaves each one its own default rather than restating nine of them
// here.
func sortFrom[T any](cmd *cobra.Command, set columnSet[T]) (ranking string, order store.Sort, err error) {
	value, err := cmd.Flags().GetString(sortFlag)
	if err != nil {
		return "", store.Sort{}, err
	}
	if strings.TrimSpace(value) == "" {
		return "", store.Sort{}, nil
	}

	var names []string
	for _, part := range strings.Split(value, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			// A trailing comma is a typo rather than a key with no name.
			return "", store.Sort{}, fmt.Errorf("--%s %q has an empty key in it", sortFlag, value)
		}
		names = append(names, name)
	}

	for _, ranking := range set.rankings {
		if names[0] != ranking {
			continue
		}
		// A ranking is the whole ordering rather than the first key of one:
		// it is already several terms deep, and what a tie in it would even
		// mean is not a question this should answer by guessing.
		if len(names) > 1 {
			return "", store.Sort{}, fmt.Errorf(
				"--%s %s is an ordering of its own and takes no further keys; "+
					"sort by columns instead, or by %s alone",
				sortFlag, ranking, ranking)
		}
		return ranking, store.Sort{}, nil
	}

	keys, err := set.sortKeys(names)
	if err != nil {
		return "", store.Sort{}, err
	}
	sort, err := store.NewSort(set.recordOf(set.blank), keys)
	return "", sort, err
}

// snoozeCell renders a snooze date, saying so when it has arrived.
//
// The queue now contains expired snoozes, so a bare date in that column would
// leave the reader to notice the year themselves. "due" is the word the status
// page uses for the same state.
//
// Compared against a timestamp rather than today's date, which is what the
// query does — see store.TimeFormat.
// Empty when there is no snooze, which is how a render function says there is
// nothing here — see orAbsent, which spells that for whichever format is
// asking.
func snoozeCell(until sql.NullString, now string) string {
	if !until.Valid || until.String == "" {
		return ""
	}
	if until.String < now {
		return "due " + until.String
	}
	return until.String
}

// dateCell is a timestamp at the length a date is read, and empty when there
// is none.
func dateCell(v sql.NullString) string {
	if !v.Valid || v.String == "" {
		return ""
	}
	return shortDate(v.String)
}
