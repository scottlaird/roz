// Package server serves the status page over HTTP.
//
// It holds no opinion about what the page contains: the caller supplies a
// function that produces one, and it is the same function `todo render`
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

// Server serves the status page. It satisfies service.Service, so `todo
// serve` runs it beside the syncer and the log tailer.
type Server struct {
	addr string
	page Page
	log  io.Writer

	// ready is closed once the listener is up, which is how a caller that
	// asked for port 0 finds out what it got.
	ready    chan struct{}
	listenOn net.Addr
}

// New returns a server. An empty addr means DefaultAddr; log may be nil.
func New(addr string, page Page, log io.Writer) *Server {
	if addr == "" {
		addr = DefaultAddr
	}
	return &Server{addr: addr, page: page, log: log, ready: make(chan struct{})}
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
	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

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

func (s *Server) logf(format string, args ...any) {
	if s.log == nil {
		return
	}
	fmt.Fprintf(s.log, format, args...)
}
