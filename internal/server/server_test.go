package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// running starts a server on a port the operating system picks, and returns
// its base URL. It stops when the test ends.
func running(t *testing.T, page Page, log io.Writer) string {
	t.Helper()

	s := New("127.0.0.1:0", page, nil, log)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run() returned error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("the server did not stop")
		}
	})

	waiting, waitCancel := context.WithTimeout(ctx, 5*time.Second)
	defer waitCancel()
	addr, err := s.Addr(waiting)
	if err != nil {
		t.Fatalf("Addr() returned error: %v", err)
	}
	return "http://" + addr.String()
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s returned error: %v", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	return resp, string(body)
}

func TestServesThePage(t *testing.T) {
	base := running(t, func(context.Context) ([]byte, error) {
		return []byte("<h1>the page</h1>"), nil
	}, nil)

	resp, body := get(t, base+"/")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if body != "<h1>the page</h1>" {
		t.Errorf("body = %q", body)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want html", got)
	}
	// The page is a view of a database that moves underneath it.
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// TestBuiltPerRequest: the point of rendering on demand is that a reload
// shows what is true now.
func TestBuiltPerRequest(t *testing.T) {
	var mu sync.Mutex
	var calls int
	base := running(t, func(context.Context) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return fmt.Appendf(nil, "page %d", calls), nil
	}, nil)

	if _, body := get(t, base+"/"); body != "page 1" {
		t.Errorf("first body = %q", body)
	}
	if _, body := get(t, base+"/"); body != "page 2" {
		t.Errorf("second body = %q, want it rebuilt", body)
	}
}

// TestOnlyTheRootPath: a mistyped path should be visibly wrong rather than
// quietly serving the page under a name that does not exist.
func TestOnlyTheRootPath(t *testing.T) {
	base := running(t, func(context.Context) ([]byte, error) {
		return []byte("the page"), nil
	}, nil)

	resp, body := get(t, base+"/queue")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if strings.Contains(body, "the page") {
		t.Error("the page was served under a path that does not exist")
	}
}

// TestRenderFailureIsA500AndLogged: one broken request must not take the
// server down, since the syncer running beside it would go too.
func TestRenderFailureIsA500AndLogged(t *testing.T) {
	var log lockedBuffer
	base := running(t, func(context.Context) ([]byte, error) {
		return nil, errors.New("the database is on fire")
	}, &log)

	resp, _ := get(t, base+"/")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	if !strings.Contains(log.String(), "on fire") {
		t.Errorf("the failure was not logged: %q", log.String())
	}

	// Still serving.
	if resp, _ := get(t, base+"/"); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("second status = %d, want the server still answering", resp.StatusCode)
	}
}

// lockedBuffer is a bytes.Buffer the server goroutine writes and the test
// reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestCancellationIsNotAFailure: the service runner takes an error as a
// reason to stop everything else, so stopping on request must return nil.
func TestCancellationIsNotAFailure(t *testing.T) {
	s := New("127.0.0.1:0", func(context.Context) ([]byte, error) {
		return []byte("the page"), nil
	}, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	waiting, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	if _, err := s.Addr(waiting); err != nil {
		t.Fatalf("Addr() returned error: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run() returned %v on cancellation, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not stop")
	}
}

func TestRunNeedsAPage(t *testing.T) {
	s := New("127.0.0.1:0", nil, nil, nil)
	if err := s.Run(context.Background()); err == nil {
		t.Error("Run() with no page returned nil, want an error")
	}
}

// TestAddressInUseIsReported: two `roz serve` processes should not leave the
// second one silently serving nothing.
func TestAddressInUseIsReported(t *testing.T) {
	page := func(context.Context) ([]byte, error) { return []byte("the page"), nil }
	base := running(t, page, nil)
	addr := strings.TrimPrefix(base, "http://")

	second := New(addr, page, nil, nil)
	if err := second.Run(context.Background()); err == nil {
		t.Error("the second server started on a taken address, want an error")
	}
}

// runningLive is running with an event stream behind it.
func runningLive(t *testing.T, changes Changes, log io.Writer) string {
	t.Helper()

	page := func(context.Context) ([]byte, error) { return []byte("the page"), nil }
	s := New("127.0.0.1:0", page, changes, log)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the server did not stop")
		}
	})

	waiting, waitCancel := context.WithTimeout(ctx, 5*time.Second)
	defer waitCancel()
	addr, err := s.Addr(waiting)
	if err != nil {
		t.Fatalf("Addr() returned error: %v", err)
	}
	return "http://" + addr.String()
}

// version is a change source a test can move.
type version struct {
	mu sync.Mutex
	at int64
	// failing makes the next read fail, standing in for a busy database.
	failing bool
}

func (v *version) read(context.Context) (int64, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.failing {
		return 0, errors.New("busy")
	}
	return v.at, nil
}

func (v *version) move() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.at++
}

func (v *version) fail(failing bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.failing = failing
}

// TestEventsStreamOnChange is what makes a page left open worth leaving open.
func TestEventsStreamOnChange(t *testing.T) {
	v := &version{at: 7}
	base := runningLive(t, v.read, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/events", nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events returned error: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}

	// Nothing has moved, so nothing should arrive; then something moves.
	v.move()

	line := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				line <- scanner.Text()
				return
			}
		}
		close(line)
	}()

	select {
	case got, ok := <-line:
		if !ok {
			t.Fatal("the stream ended without an event")
		}
		if got != "data: 8" {
			t.Errorf("event = %q, want the new version", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no event arrived after the version moved")
	}
}

// TestEventsSurviveAFailedRead: the database being busy for a moment is not a
// reason to drop a connection the browser would then have to rebuild.
func TestEventsSurviveAFailedRead(t *testing.T) {
	v := &version{at: 1}
	base := runningLive(t, v.read, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events returned error: %v", err)
	}
	defer resp.Body.Close()

	v.fail(true)
	time.Sleep(2 * pollInterval)
	v.fail(false)
	v.move()

	line := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				line <- scanner.Text()
				return
			}
		}
		close(line)
	}()

	select {
	case got, ok := <-line:
		if !ok {
			t.Fatal("the stream ended after a failed read")
		}
		if got != "data: 2" {
			t.Errorf("event = %q, want the version after the failure", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no event arrived after the read recovered")
	}
}

// TestEventsSurviveAFailedFirstRead: the read that takes the baseline is no
// different from any later one, so a database busy at connect time should not
// cost the client its connection.
func TestEventsSurviveAFailedFirstRead(t *testing.T) {
	v := &version{at: 5, failing: true}
	base := runningLive(t, v.read, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events returned error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: the stream should open anyway", resp.StatusCode)
	}

	lines := make(chan string, 4)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				lines <- scanner.Text()
			}
		}
	}()

	// The read recovers with nothing having changed. That first success is the
	// baseline, not news, and sending it would reload the page over a change
	// nobody made.
	v.fail(false)
	select {
	case got, ok := <-lines:
		if ok {
			t.Fatalf("event = %q, want none: the baseline is not a change", got)
		}
		t.Fatal("the stream ended after a failed first read")
	case <-time.After(3 * pollInterval):
	}

	v.move()
	select {
	case got, ok := <-lines:
		if !ok {
			t.Fatal("the stream ended before the change arrived")
		}
		if got != "data: 6" {
			t.Errorf("event = %q, want the version after the change", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no event arrived after the version moved")
	}
}

// TestNoEventsWithoutAChangeSource: `roz render` has no server, and a server
// with nothing to watch should say so rather than hold a connection open
// promising events that cannot come.
func TestNoEventsWithoutAChangeSource(t *testing.T) {
	base := running(t, func(context.Context) ([]byte, error) {
		return []byte("the page"), nil
	}, nil)

	resp, _ := get(t, base+"/events")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestStoppingWithAStreamOpenIsPrompt is the interrupt path, which reported
// "context deadline exceeded" after a few seconds for as long as the server
// has existed.
//
// Shutdown waits for requests in flight and does not cancel their contexts, so
// an event stream — which is meant to last as long as the page is open — was
// waited out for the whole grace period and then given up on. With a browser
// open, every Ctrl-C looked like a failure.
func TestStoppingWithAStreamOpenIsPrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := New("127.0.0.1:0",
		func(context.Context) ([]byte, error) { return []byte("page"), nil },
		func(context.Context) (int64, error) { return 1, nil },
		nil)

	stopped := make(chan error, 1)
	go func() { stopped <- s.Run(ctx) }()

	waiting, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	addr, err := s.Addr(waiting)
	if err != nil {
		t.Fatalf("Addr() returned error: %v", err)
	}

	// A client holding the stream open, as a browser with the page open does.
	req, err := http.NewRequest(http.MethodGet, "http://"+addr.String()+"/events", nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("opening the stream: %v", err)
	}
	defer resp.Body.Close()
	// Read the headers' worth, so the handler is certainly inside its loop
	// rather than still starting up.
	buf := make([]byte, 1)
	go func() { _, _ = resp.Body.Read(buf) }()

	started := time.Now()
	cancel()

	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("Run() returned %v, want a clean stop", err)
		}
	case <-time.After(shutdownGrace):
		t.Fatal("the server waited out the whole grace period with a stream open")
	}
	// Prompt, not merely inside the grace period: the point is that it does
	// not wait for a stream that would never end on its own.
	if waited := time.Since(started); waited > shutdownGrace/2 {
		t.Errorf("stopping took %v, which is most of the %v grace", waited, shutdownGrace)
	}
}

// TestMetricsAreServed: the exposition sits on the same listener as the page,
// which is loopback and unauthenticated. That is the right trade and worth a
// test — what it carries is less sensitive than the page beside it, so making
// it harder to reach than the thing it describes would gain nothing.
func TestMetricsAreServed(t *testing.T) {
	base := running(t, func(context.Context) ([]byte, error) {
		return []byte("the page"), nil
	}, nil)

	resp, body := get(t, base+"/metrics")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "go_goroutines") {
		t.Errorf("the exposition is missing the runtime:\n%s", body)
	}
	if strings.Contains(body, "the page") {
		t.Error("the page was served under /metrics")
	}
}
