package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/ghsync"
	"github.com/scottlaird/roz/internal/mcp"
	"github.com/scottlaird/roz/internal/metrics"
	"github.com/scottlaird/roz/internal/server"
	"github.com/scottlaird/roz/internal/service"
	"github.com/scottlaird/roz/internal/store"
)

const flagAddr = "addr"

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Sync, tail the log and serve the status page, until interrupted",
		Long: "The three long-running pieces in one command: `roz syncer`, `roz\n" +
			"watch` and a web server for the page `roz render` produces.\n\n" +
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
	f.Bool(flagMCP, false,
		"also serve MCP at /mcp, so an agent survives a restart of this process")
	f.String(flagAgent, "mcp", "actor name for an MCP client that does not identify itself")
	f.Bool(flagNoWatch, false, "serve and poll, but do not print the log")
	return cmd
}

const (
	flagNoSync  = "no-sync"
	flagMCP     = "mcp"
	flagNoWatch = "no-watch"
)

func runServe(cmd *cobra.Command, _ []string) error {
	options, err := serveOptionsFrom(cmd)
	if err != nil {
		return err
	}

	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	// Recorded once, at startup, because that is what it means: a serve reads
	// the schema when it opens the database and keeps serving that schema
	// until it is restarted. A gauge that re-read the file would report the
	// database's version rather than this process's, which is the opposite of
	// the question — "is what is running still current" — that it is for.
	if version, err := st.SchemaVersion(cmd.Context()); err == nil {
		metrics.SchemaVersion(version)
	}

	// Ctrl-C stops every service cleanly, the way `roz watch` does.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	web, err := httpServer(cmd, st, options)
	if err != nil {
		return err
	}
	services := []service.Service{
		// First because it governs the rest: if the schema moves, none of them
		// should keep running, and the service runner stops the others as soon
		// as this one returns an error.
		&schemaGuard{store: st},
		web,
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

// pageFor builds the same page `roz render` writes, per request. One
// renderer rather than two, since two would eventually disagree.
//
// Live, because this one has a server behind it to tell it when to reload.
func pageFor(st *store.Store) server.Page {
	return func(ctx context.Context, at server.PageQuery) ([]byte, error) {
		body, err := renderRoute(ctx, st, time.Now(), true,
			route{kind: at.Kind, id: at.ID, view: at.View})
		if errors.Is(err, errNoSuchPage) {
			// The server's word for it, so it can answer 404 without knowing
			// what the cli package calls the condition.
			return nil, server.ErrNoPage
		}
		return body, err
	}
}

type serveOptions struct {
	addr           string
	interval       time.Duration
	rateLimitFloor int
	maxFailures    int
	noSync         bool
	mcp            bool
	mcpAgent       string
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
	if o.mcp, err = f.GetBool(flagMCP); err != nil {
		return o, err
	}
	if o.mcpAgent, err = f.GetString(flagAgent); err != nil {
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

// tailer follows the log as a service, so `roz serve` prints what changed
// without anyone running `roz watch` beside it.
//
// It starts at the end rather than replaying a backlog: what happened before
// the process started is history, and `roz watch --since` is how to read it.
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

// httpServer builds the page server, with the MCP endpoint on it when asked.
//
// The endpoint is opt-in because of what it exposes rather than what it costs.
// Stdio is reachable only by the process that spawned it; this is reachable by
// anything running locally, and these are write tools. Origin validation stops
// a browser being turned into a client and does nothing about a local process,
// so the honest default is off.
func httpServer(cmd *cobra.Command, st *store.Store, options serveOptions) (service.Service, error) {
	srv := server.New(options.addr, pageFor(st), st.LatestEventSeq, cmd.ErrOrStderr())
	if !options.mcp {
		return srv, nil
	}
	// Resolved once here rather than read per call, the way `roz mcp` does it:
	// the tool tree is handed the path, so nothing it does depends on a flag
	// this process might parse again.
	path, err := dbPathFrom(cmd)
	if err != nil {
		return nil, err
	}
	return srv.WithMCP(&mcp.HTTP{Server: &mcp.Server{
		Name:    "roz",
		Version: version,
		Tools:   &mcpTools{db: path, agent: actorName(options.mcpAgent)},
		Log:     cmd.ErrOrStderr(),
	}}), nil
}
