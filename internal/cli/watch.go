package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

const (
	flagLines    = "lines"
	flagSince    = "since"
	flagInterval = "interval"
	flagSeverity = "severity"
	flagKind     = "kind"
	flagOnce     = "once"
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
			"the output can be consumed as it arrives.",
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
	addOutputFlag(cmd)
	cmd.MarkFlagsMutuallyExclusive(flagLines, flagSince)

	return cmd
}

func runWatch(cmd *cobra.Command, _ []string) error {
	options, err := watchOptionsFrom(cmd)
	if err != nil {
		return err
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	// Ctrl-C ends the follow loop without an error, the way tail -f does.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	return watchEvents(ctx, cmd.OutOrStdout(), st, options)
}

// watchOptions is everything the loop needs, resolved from flags once.
type watchOptions struct {
	query    store.EventQuery
	backlog  bool
	interval time.Duration
	format   string
	once     bool
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

	query := store.EventQuery{Severity: severity, Kind: kind}
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
	}, nil
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
		printedTo, err := printEvents(ctx, out, st, query, options.format)
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
			cursor, err = printEvents(ctx, out, st, query, options.format)
			if err != nil {
				return err
			}
		}
	}
}

// printEvents writes one batch and returns the cursor to resume from, which
// is the previous cursor when nothing matched.
func printEvents(ctx context.Context, out io.Writer, st *store.Store, query store.EventQuery, format string) (int64, error) {
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
		if err := writeEvent(out, e, format); err != nil {
			return 0, err
		}
		cursor = e.Seq
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
