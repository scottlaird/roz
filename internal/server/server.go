// Package server serves the status page over HTTP.
//
// It holds no opinion about what the page contains: the caller supplies a
// function that produces one, and it is the same function `roz render`
// writes to a file. Two ways of building the page would eventually be two
// different pages.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/scottlaird/roz/internal/metrics"
	"github.com/scottlaird/roz/internal/static"
)

// DefaultAddr is where the server listens unless told otherwise. Loopback
// rather than every interface: this is a personal queue, it has no
// authentication, and it should not become reachable by accident.
const DefaultAddr = "127.0.0.1:8737"

// shutdownGrace is how long a request in flight has to finish once the
// server has been asked to stop.
const shutdownGrace = 5 * time.Second

// Page produces the page to serve. It is called per request, so what is
// served is never staler than the request that asked for it.
type Page func(ctx context.Context) ([]byte, error)

// Changes reports a version of the world that moves whenever something a
// page would show has changed. The server does not care what the number
// means, only that it differs; `roz serve` passes the log's head, since
// nothing changes in this system without an event.
type Changes func(ctx context.Context) (int64, error)

// pollInterval is how often a live connection asks whether anything moved.
// The same second `roz watch` uses, for the same reason: it is the shortest
// gap that is not really polling.
const pollInterval = time.Second

// keepalive stops an idle stream from looking dead to anything between the
// server and the browser. On loopback nothing needs it; it costs one line a
// minute and means the endpoint behaves the same behind a proxy.
const keepalive = 30 * time.Second

// Server serves the status page. It satisfies service.Service, so `roz
// serve` runs it beside the syncer and the log tailer.
type Server struct {
	addr    string
	page    Page
	changes Changes
	log     io.Writer

	// ready is closed once the listener is up, which is how a caller that
	// asked for port 0 finds out what it got.
	ready    chan struct{}
	listenOn net.Addr

	// streams is cancelled when the server begins shutting down, which is how
	// a long-lived response learns to end.
	//
	// Shutdown waits for requests in flight and does not cancel their
	// contexts, so a handler that only watches its own request context will
	// sit there until the grace period runs out. An event stream is exactly
	// that: it is meant to last as long as the page is open.
	//
	// Assigned before the listener is served, so no handler can observe it
	// unset.
	streams context.Context
}

// New returns a server.
//
// An empty addr means DefaultAddr, and log may be nil. changes may be nil
// too, in which case there is no event stream and a page served from here
// does not know when to reload.
func New(addr string, page Page, changes Changes, log io.Writer) *Server {
	if addr == "" {
		addr = DefaultAddr
	}
	return &Server{
		addr: addr, page: page, changes: changes, log: log,
		ready: make(chan struct{}),
	}
}

func (s *Server) Name() string { return "server" }

// Run serves until ctx is cancelled, then gives requests in flight a moment
// to finish. Cancellation is not a failure, so it returns nil.
func (s *Server) Run(ctx context.Context) error {
	if s.page == nil {
		return errors.New("no page to serve")
	}

	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.addr, err)
	}
	s.listenOn = listener.Addr()
	close(s.ready)
	s.logf("serving http://%s/\n", s.listenOn)

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	mux.HandleFunc("/events", s.handleEvents)
	// Served from the same listener as the page, which is loopback and
	// unauthenticated. That is the right trade here and worth saying: what
	// this exposes -- request counts, a rate-limit budget, a schema version --
	// is less sensitive than the page beside it, so nothing is gained by
	// making it harder to reach than the thing it describes.
	mux.Handle("/metrics", metrics.Handler())
	// Compiled in, and answered with an ETag, so a browser fetches the
	// stylesheet once and is told 304 for the rest of the process's life —
	// and for the next one too, since the tag is the content's hash rather
	// than a start time.
	mux.Handle(static.Prefix, static.Handler())
	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	// Told to stop before Shutdown starts waiting, so an event stream ends of
	// its own accord rather than being waited out. Without this, interrupting
	// the server with a page open takes the whole grace period and then fails
	// with "context deadline exceeded" — the shutdown reporting as an error
	// what was really just a browser doing what it was told.
	streams, endStreams := context.WithCancel(context.WithoutCancel(ctx))
	defer endStreams()
	s.streams = streams
	httpServer.RegisterOnShutdown(endStreams)

	failed := make(chan error, 1)
	go func() {
		err := httpServer.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
		close(failed)
	}()

	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}

	// Shutdown needs a context of its own: the one that stopped us is
	// already cancelled, and would cut off the requests we are waiting for.
	stopping, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	return httpServer.Shutdown(stopping)
}

// Addr reports where the server is listening, waiting until it is up. It
// exists for a caller that asked for port 0, which includes every test.
func (s *Server) Addr(ctx context.Context) (net.Addr, error) {
	select {
	case <-s.ready:
		return s.listenOn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// handle serves the page, and only the page. Anything else is a 404 rather
// than the page under a wrong name, so a mistyped path is visible.
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	page, err := s.page(r.Context())
	if err != nil {
		s.logf("rendering: %v\n", err)
		http.Error(w, "the page could not be built", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The page is a view of a database that changes underneath it, so a
	// cached copy is worse than no page at all.
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write(page); err != nil {
		s.logf("writing the page: %v\n", err)
	}
}

// handleEvents streams a line whenever the world moves, so a page left open
// can reload itself.
//
// Server-sent events rather than a websocket: the page only ever listens, the
// browser reconnects on its own, and it is a few lines of the standard
// library against a dependency and a handshake.
//
// Each connection polls on its own. That is one query a second per open tab,
// which is the wrong shape at scale and entirely fine for a personal queue on
// loopback; sharing one poller between connections is the fix if it ever
// stops being fine.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.changes == nil {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported here", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()

	// The baseline is taken before the headers go out, so that a client
	// holding a response is a client whose starting version is already known.
	// The other order is a race: the response arrives, something moves, and
	// only then does this read happen — picking up the new version as the
	// baseline and reporting the change as nothing at all.
	//
	// A failed read here is treated exactly as the loop below treats one. It
	// used to end the connection, which is the same busy database the comment
	// down there says is not worth dropping a connection over, and the browser
	// would only rebuild it against that same database.
	last, known := int64(0), true
	if current, err := s.changes(ctx); err != nil {
		s.logf("reading the version: %v\n", err)
		known = false
	} else {
		last = current
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	idle := time.NewTicker(keepalive)
	defer idle.Stop()

	// nil where a handler is exercised without Serve, which no shutdown can
	// reach anyway: a nil channel blocks for ever, so the select simply never
	// takes this arm.
	var ending <-chan struct{}
	if s.streams != nil {
		ending = s.streams.Done()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ending:
			// The server is shutting down. The browser reconnects on its own
			// when there is something to reconnect to, so there is nothing to
			// say — and saying it would only race the socket closing.
			return
		case <-idle.C:
			fmt.Fprint(w, ": still here\n\n")
			flusher.Flush()
		case <-ticker.C:
			current, err := s.changes(ctx)
			if err != nil {
				// A failed read is not a reason to drop the connection: the
				// database may be busy, and the next tick will try again.
				continue
			}
			if !known {
				// The baseline never arrived, so this is it. Sending it would
				// reload the page over a change nobody made.
				last, known = current, true
				continue
			}
			if current == last {
				continue
			}
			last = current
			fmt.Fprintf(w, "data: %d\n\n", current)
			flusher.Flush()
		}
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.log == nil {
		return
	}
	fmt.Fprintf(s.log, format, args...)
}
