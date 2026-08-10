package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/ghsync"
	"github.com/scottlaird/todo/internal/server"
	"github.com/scottlaird/todo/internal/service"
	"github.com/scottlaird/todo/internal/store"
)

const flagAddr = "addr"

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Sync, tail the log and serve the status page, until interrupted",
		Long: "The three long-running pieces in one command: `todo syncer`, `todo\n" +
			"watch` and a web server for the page `todo render` produces.\n\n" +
			"They are separate services sharing a process, so the first one to fail\n" +
			"stops the others rather than leaving a half-working system that looks\n" +
			"fine. Ctrl-C stops all three.\n\n" +
			"The page is built per request, so it is never staler than the request\n" +
			"that asked for it, and a browser left open reloads itself when the log\n" +
			"moves — the server streams a line on /events and the page listens.\n\n" +
			"It listens on loopback and has no authentication. Do not put it on an\n" +
			"interface anyone else can reach.",
		Args: cobra.NoArgs,
		RunE: runServe,
	}

	f := cmd.Flags()
	f.String(flagAddr, server.DefaultAddr, "address to listen on")
	f.Duration(flagInterval, ghsync.DefaultInterval, "gap between GitHub polls")
	f.Int(flagRateLimitFloor, ghsync.DefaultRateLimitFloor,
		"remaining rate limit below which polling is paced out")
	f.Int(flagMaxFailures, ghsync.DefaultMaxFailures,
		"consecutive sync failures to tolerate before giving up; 0 to keep trying")
	f.Bool(flagNoSync, false, "serve and tail, but do not poll GitHub")
	f.Bool(flagNoWatch, false, "serve and poll, but do not print the log")
	return cmd
}

const (
	flagNoSync  = "no-sync"
	flagNoWatch = "no-watch"
)

func runServe(cmd *cobra.Command, _ []string) error {
	options, err := serveOptionsFrom(cmd)
	if err != nil {
		return err
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	// Ctrl-C stops every service cleanly, the way `todo watch` does.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	services := []service.Service{
		server.New(options.addr, pageFor(st), st.LatestEventSeq, cmd.ErrOrStderr()),
	}
	if !options.noSync {
		services = append(services, &ghsync.Syncer{
			Store:          st,
			Client:         newFetcher(),
			Interval:       options.interval,
			RateLimitFloor: options.rateLimitFloor,
			MaxFailures:    options.maxFailures,
			Log:            cmd.ErrOrStderr(),
		})
	}
	if !options.noWatch {
		services = append(services, &tailer{store: st, out: cmd.OutOrStdout()})
	}
	return service.Run(ctx, services...)
}

// pageFor builds the same page `todo render` writes, per request. One
// renderer rather than two, since two would eventually disagree.
//
// Live, because this one has a server behind it to tell it when to reload.
func pageFor(st *store.Store) server.Page {
	return func(ctx context.Context) ([]byte, error) {
		return renderPage(ctx, st, time.Now(), true)
	}
}

type serveOptions struct {
	addr           string
	interval       time.Duration
	rateLimitFloor int
	maxFailures    int
	noSync         bool
	noWatch        bool
}

func serveOptionsFrom(cmd *cobra.Command) (serveOptions, error) {
	f := cmd.Flags()
	var o serveOptions
	var err error

	if o.addr, err = f.GetString(flagAddr); err != nil {
		return o, err
	}
	if o.interval, err = f.GetDuration(flagInterval); err != nil {
		return o, err
	}
	if o.interval <= 0 {
		return o, fmt.Errorf("--%s must be positive, not %s", flagInterval, o.interval)
	}
	if o.rateLimitFloor, err = f.GetInt(flagRateLimitFloor); err != nil {
		return o, err
	}
	if o.maxFailures, err = f.GetInt(flagMaxFailures); err != nil {
		return o, err
	}
	if o.noSync, err = f.GetBool(flagNoSync); err != nil {
		return o, err
	}
	if o.noWatch, err = f.GetBool(flagNoWatch); err != nil {
		return o, err
	}
	return o, nil
}

// tailer follows the log as a service, so `todo serve` prints what changed
// without anyone running `todo watch` beside it.
//
// It starts at the end rather than replaying a backlog: what happened before
// the process started is history, and `todo watch --since` is how to read it.
type tailer struct {
	store *store.Store
	out   io.Writer
}

func (t *tailer) Name() string { return "watch" }

func (t *tailer) Run(ctx context.Context) error {
	return watchEvents(ctx, t.out, t.store, watchOptions{
		interval: time.Second,
		format:   outputTable,
	})
}
