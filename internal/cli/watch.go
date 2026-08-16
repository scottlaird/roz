package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/filter"
	"github.com/scottlaird/roz/internal/service"
	"github.com/scottlaird/roz/internal/store"
)

const (
	flagLines    = "lines"
	flagSince    = "since"
	flagInterval = "interval"
	flagSeverity = "severity"
	flagKind     = "kind"
	flagOnce     = "once"

	flagExcludeActor = "exclude-actor"
)

const (
	defaultLines    = 10
	defaultInterval = time.Second
)

func newWatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Follow the event log",
		Long: "Prints the tail of the log and then follows it, like tail -f.\n\n" +
			"SQLite has no way to push a notification, so this polls. --interval\n" +
			"controls how often; the cost of a poll is one indexed query against\n" +
			"seq, so a short interval is cheap.\n\n" +
			"With -o json each event is a separate line rather than one array, so\n" +
			"the output can be consumed as it arrives.\n\n" +
			"--exclude-actor drops one writer's events, which is what an agent\n" +
			"watching its own queue wants: its writes are the noise and everything\n" +
			"else is the signal. It is an exclusion rather than a selection\n" +
			"because the actor vocabulary is open, so \"everything but mine\"\n" +
			"cannot be said as a list of the others.\n\n" +
			"--filter is a CEL expression over the log's own columns, for the\n" +
			"questions the flags above cannot ask: `field == \"priority\"`, or\n" +
			"`subject_id.startsWith(\"SL\") && kind == \"changed\"`. It composes\n" +
			"with them rather than replacing them. --explain-filter says how much\n" +
			"of it the query took, which matters more here than on a listing: a\n" +
			"tail re-runs its query every interval.\n\n" +
			"A question worth asking twice is worth saving. `roz view add mine\n" +
			"--entity event --filter ...` then `roz watch --view mine`: the log is\n" +
			"a view entity like any listing, and an agent's exclusion is a\n" +
			"property of the agent rather than of the invocation.",
		Args: cobra.NoArgs,
		RunE: runWatch,
	}

	f := cmd.Flags()
	f.IntP(flagLines, "n", defaultLines, "events of backlog to print first; 0 starts at the end")
	f.String(flagSince, "", "start from a date or timestamp instead of a backlog count")
	f.Duration(flagInterval, defaultInterval, "how often to poll for new events")
	f.String(flagSeverity, "", "keep only info, notice or exception")
	f.String(flagKind, "", "keep only one event kind, e.g. created or changed")
	f.Bool(flagOnce, false, "print the selected events and exit instead of following")
	f.StringSlice(flagExcludeActor, nil,
		"drop events by this actor, e.g. agent:claude; repeatable or comma-separated")
	addOutputFlag(cmd)
	addFilterFlag(cmd, eventColumns.names())
	addViewFlag(cmd)
	cmd.MarkFlagsMutuallyExclusive(flagLines, flagSince)

	// The entity is named rather than taken from the command path: `watch` is
	// a top-level command, so there is no parent to read it off the way a
	// listing has.
	cmd.PreRunE = func(cmd *cobra.Command, _ []string) error {
		return applyView(cmd, entityEvent)
	}

	return cmd
}

// eventColumns is the log's vocabulary, which is what --filter and a saved
// view are written against.
//
// The log is a filter source without being a listing: it has no --fields and
// no --sort, because a tail is one line per event in the order they happened
// and neither is a question anybody asks of it. What the registry is needed
// for is the names — a filter language has to know what can be named, and a
// view has to be checkable against the same list.
var eventColumns = columnSet[*store.Event]{
	blank: &store.Event{},
	empty: "no events",
}

func runWatch(cmd *cobra.Command, _ []string) error {
	options, err := watchOptionsFrom(cmd)
	if err != nil {
		return err
	}

	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	// Ctrl-C ends the follow loop without an error, the way tail -f does.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	// --once reads and returns, so there is no window for the schema to move
	// in and nothing to guard.
	if options.once {
		return watchEvents(ctx, cmd.OutOrStdout(), st, options)
	}
	return service.Run(ctx,
		&watcher{store: st, out: cmd.OutOrStdout(), options: options},
		&schemaGuard{store: st})
}

// watcher is the follow loop as a service, so it can be run beside the schema
// guard. `roz serve` has its own tailer, which starts at the end of the log
// rather than replaying a backlog.
type watcher struct {
	store   *store.Store
	out     io.Writer
	options watchOptions
}

func (w *watcher) Name() string { return "watch" }

func (w *watcher) Run(ctx context.Context) error {
	return watchEvents(ctx, w.out, w.store, w.options)
}

// watchOptions is everything the loop needs, resolved from flags once.
type watchOptions struct {
	query    store.EventQuery
	backlog  bool
	interval time.Duration
	format   string
	once     bool
	// filter is the compiled --filter. Its SQL half is already in query; this
	// is what runs over the rows the query could not narrow.
	filter *filter.Filter
}

func watchOptionsFrom(cmd *cobra.Command) (watchOptions, error) {
	f := cmd.Flags()

	format, err := outputFrom(cmd)
	if err != nil {
		return watchOptions{}, err
	}
	severity, err := f.GetString(flagSeverity)
	if err != nil {
		return watchOptions{}, err
	}
	if err := validateSeverity(severity); err != nil {
		return watchOptions{}, err
	}
	kind, err := f.GetString(flagKind)
	if err != nil {
		return watchOptions{}, err
	}
	interval, err := f.GetDuration(flagInterval)
	if err != nil {
		return watchOptions{}, err
	}
	if interval <= 0 {
		return watchOptions{}, fmt.Errorf("--interval must be positive, not %s", interval)
	}
	once, err := f.GetBool(flagOnce)
	if err != nil {
		return watchOptions{}, err
	}
	excluded, err := f.GetStringSlice(flagExcludeActor)
	if err != nil {
		return watchOptions{}, err
	}
	if err := validateActors(excluded, f.Changed(flagExcludeActor)); err != nil {
		return watchOptions{}, err
	}

	expr, err := filterFrom(cmd, &store.Event{})
	if err != nil {
		return watchOptions{}, err
	}

	query := store.EventQuery{Severity: severity, Kind: kind, ExcludeActors: excluded}
	// The hand-cut flags and the expression compose, the way a listing's do:
	// --kind narrows and --filter narrows again, rather than one replacing
	// the other.
	//
	// Taken before the plan is explained, because asking for the SQL is what
	// records that the query took it: explaining first would report every
	// filter as having run in Go.
	query.Where, query.WhereArgs = expr.SQL()
	if err := explainFilter(cmd, expr, nil); err != nil {
		return watchOptions{}, err
	}
	backlog := true
	if since, err := f.GetString(flagSince); err != nil {
		return watchOptions{}, err
	} else if since != "" {
		normalised, err := validateTimestamp(flagSince, since)
		if err != nil {
			return watchOptions{}, err
		}
		query.SinceAt = normalised
	} else {
		lines, err := f.GetInt(flagLines)
		if err != nil {
			return watchOptions{}, err
		}
		if lines < 0 {
			return watchOptions{}, fmt.Errorf("-n must not be negative, not %d", lines)
		}
		// Newest = 0 means "no limit" to the query, so an empty backlog has
		// to be a separate decision rather than a count of zero.
		backlog = lines > 0
		query.Newest = lines
	}

	return watchOptions{
		query:    query,
		backlog:  backlog,
		interval: interval,
		format:   format,
		once:     once,
		filter:   expr,
	}, nil
}

// validateActors rejects an empty exclusion rather than ignoring it. Nothing
// is ever written with an empty actor, so `--exclude-actor ""` hides nothing
// while leaving the caller believing they have hidden something.
//
// The empty string parses as a list of no names rather than one empty name,
// which is why this needs to know the flag was given at all.
func validateActors(actors []string, given bool) error {
	if given && len(actors) == 0 {
		return fmt.Errorf("--%s was given no name", flagExcludeActor)
	}
	for _, actor := range actors {
		if strings.TrimSpace(actor) == "" {
			return fmt.Errorf("--%s was given an empty name", flagExcludeActor)
		}
	}
	return nil
}

func validateSeverity(severity string) error {
	switch severity {
	case "", store.SeverityInfo, store.SeverityNotice, store.SeverityException:
		return nil
	default:
		return fmt.Errorf("--severity %q is not recognised: use %s, %s or %s",
			severity, store.SeverityInfo, store.SeverityNotice, store.SeverityException)
	}
}

// watchEvents prints the selected backlog and then polls for more until the
// context is cancelled.
func watchEvents(ctx context.Context, out io.Writer, st *store.Store, options watchOptions) error {
	query := options.query

	// Take the log's head before printing anything. The backlog is filtered
	// and may be empty, so resuming from whatever it printed would replay the
	// whole log on the first poll — most visibly with a --since in the future.
	cursor, err := st.LatestEventSeq(ctx)
	if err != nil {
		return err
	}

	if options.backlog {
		printedTo, err := printEvents(ctx, out, st, query, options)
		if err != nil {
			return err
		}
		// An event written while the backlog was printing can land past the
		// head we captured; keep whichever is further on.
		if printedTo > cursor {
			cursor = printedTo
		}
	}
	if options.once {
		return nil
	}

	// After the backlog the query becomes a pure cursor walk: the backlog
	// bounds are one-shot and must not be reapplied on every poll.
	query.Newest = 0
	query.SinceAt = ""

	ticker := time.NewTicker(options.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			query.AfterSeq = cursor
			cursor, err = printEvents(ctx, out, st, query, options)
			if err != nil {
				return err
			}
		}
	}
}

// printEvents writes one batch and returns the cursor to resume from, which
// is the previous cursor when nothing matched.
//
// The cursor advances over every row read, not only the ones printed. A filter
// that hides an event must not make the tail read it again on the next poll,
// which is what a cursor taken from the last printed line would do.
func printEvents(ctx context.Context, out io.Writer, st *store.Store, query store.EventQuery, options watchOptions) (int64, error) {
	events, err := st.Events(ctx, query)
	if err != nil {
		// A cancelled context during shutdown is not a failure.
		if ctx.Err() != nil {
			return query.AfterSeq, nil
		}
		return 0, err
	}

	cursor := query.AfterSeq
	for _, e := range events {
		cursor = e.Seq
		// Whatever the query could not take. Nothing is pushed down when the
		// expression names a column SQLite cannot compare the way CEL does,
		// and --explain-filter is what says which happened.
		keep, err := options.filter.Keep(e)
		if err != nil {
			return 0, err
		}
		if !keep {
			continue
		}
		if err := writeEvent(out, e, options.format); err != nil {
			return 0, err
		}
	}
	return cursor, nil
}

func writeEvent(out io.Writer, e *store.Event, format string) error {
	if format == outputJSON {
		// One object per line: an array could never be closed on a tail.
		encoded, err := store.MarshalRecord(e)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(encoded))
		return err
	}

	_, err := fmt.Fprintf(out, "%-24s  %-9s  %-13s  %-9s  %-12s  %s\n",
		e.At, e.Severity, e.Actor, e.Kind, e.SubjectID, eventDetail(e))
	return err
}

// eventDetail is the part of a line that varies by event kind: a field
// change, a note, or nothing for a bare lifecycle event.
func eventDetail(e *store.Event) string {
	switch {
	case e.Field != "":
		return fmt.Sprintf("%s: %q → %q", e.Field, e.OldValue, e.NewValue)
	case e.Note != "":
		return e.Note
	default:
		return ""
	}
}
