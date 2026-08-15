package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/filter"
)

// compiledFilter is where a command's filter is kept once it is built.
//
// One per invocation, because two of them would disagree about what happened:
// `pr list` compiles a filter to put its SQL half in the query, and the shared
// listing tail compiles one to run whatever is left. If those are separate
// objects, the second does not know the first pushed anything down, and either
// re-runs the whole expression in Go or — worse, if it assumed the opposite —
// filters nothing at all.
type filterKey struct{}

type compiled struct{ f *filter.Filter }

const (
	flagFilter = "filter"
	// flagExplainFilter shows how the filter was divided. Off by default —
	// the answer is the listing, not the plan — but the plan is what says
	// whether the query did the work or Go did.
	flagExplainFilter = "explain-filter"
)

// addFilterFlag registers --filter on a listing that declares its columns.
//
// The vocabulary is the record's own column names, which is the same one
// --fields and --sort accept. That is the argument for having built the
// registry before this: a filter language needs to know what can be named, and
// nothing here has to be told.
func addFilterFlag(cmd *cobra.Command, names []string) {
	cmd.Flags().String(flagFilter, "",
		"a CEL expression over the columns, e.g. 'state == \"MERGED\" || state == \"CLOSED\"'")
	cmd.Flags().Bool(flagExplainFilter, false,
		"say how much of the filter ran in SQL and how much in Go")
}

// filterFrom compiles --filter against the record a listing holds, once per
// command invocation.
func filterFrom(cmd *cobra.Command, blank any) (*filter.Filter, error) {
	if held, ok := cmd.Context().Value(filterKey{}).(compiled); ok {
		return held.f, nil
	}
	expr, err := cmd.Flags().GetString(flagFilter)
	if err != nil {
		return nil, err
	}
	f, err := filter.Compile(blank, expr)
	if err != nil {
		return nil, err
	}
	cmd.SetContext(context.WithValue(cmd.Context(), filterKey{}, compiled{f: f}))
	return f, nil
}

// explainFilter prints the plan when asked.
func explainFilter(cmd *cobra.Command, f *filter.Filter) error {
	if f == nil {
		return nil
	}
	asked, err := cmd.Flags().GetBool(flagExplainFilter)
	if err != nil || !asked {
		return err
	}
	fmt.Fprintln(cmd.ErrOrStderr(), f.Explain())
	return nil
}

// keep applies whatever part of the filter the query could not.
//
// Over the rows the store returned, which is the whole table where nothing was
// pushed down. That is the cost this exists to make visible rather than to
// hide: --explain-filter says which happened.
func keep[T any](f *filter.Filter, rows []T, record func(T) any) ([]T, error) {
	if f == nil {
		return rows, nil
	}
	kept := make([]T, 0, len(rows))
	for _, row := range rows {
		ok, err := f.Keep(record(row))
		if err != nil {
			return nil, err
		}
		if ok {
			kept = append(kept, row)
		}
	}
	return kept, nil
}
