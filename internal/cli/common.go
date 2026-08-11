package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

const actorFlag = "actor"

// addActorFlag registers --actor on a command that writes.
func addActorFlag(cmd *cobra.Command) {
	cmd.Flags().String(actorFlag, string(store.ActorHuman),
		"who is making the change: human, or agent:<name>")
}

// actorFrom reads and validates --actor.
//
// sync: actors are refused here. They are set by `todo sync`, and letting a
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
		return "", fmt.Errorf("--actor %s is not allowed: sync actors are set by `todo sync`, "+
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

// openStore opens the database named by --db, translating an uninitialised
// database into advice rather than a missing-table error.
func openStore() (*store.Store, error) {
	if dbPath == "" {
		return nil, errors.New("no database path: pass --db or set TODO_DB")
	}
	st, err := store.OpenStore(dbPath)
	if err != nil {
		if errors.Is(err, store.ErrNotInitialised) {
			return nil, fmt.Errorf("%w; run `todo init` first", err)
		}
		return nil, err
	}
	return st, nil
}

// Sort orders a list command accepts. The default everywhere is creation
// order, which is honest about being arbitrary; asking for priority is a
// choice the caller makes.
const (
	sortFlag      = "sort"
	sortCreated   = "created"
	sortPriority  = "priority"
	sortStaleness = "staleness"
)

// addSortFlag registers --sort on a list command.
func addSortFlag(cmd *cobra.Command) {
	cmd.Flags().String(sortFlag, sortCreated,
		"order: created, priority, or staleness — longest un-verified first")
}

// sortFrom resolves --sort into the store's ordering.
func sortFrom(cmd *cobra.Command) (string, error) {
	value, err := cmd.Flags().GetString(sortFlag)
	if err != nil {
		return "", err
	}
	switch value {
	case sortCreated:
		return store.OrderCreated, nil
	case sortPriority:
		return store.OrderPriority, nil
	case sortStaleness:
		return store.OrderStaleness, nil
	default:
		return "", fmt.Errorf("--%s %q is not an order: use %s, %s or %s",
			sortFlag, value, sortCreated, sortPriority, sortStaleness)
	}
}
