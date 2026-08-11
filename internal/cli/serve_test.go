package cli

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scottlaird/roz/internal/github"
)

// TestServeServesTheRenderedPage is the join: what the server hands out is
// what `roz render` writes, because it is the same function.
func TestServeServesTheRenderedPage(t *testing.T) {
	db := renderedFixture(t)
	withFetcher(t, stubFetcher{result: github.Result{}})

	base := serving(t, db, "--no-sync", "--no-watch")

	body := fetch(t, base+"/")
	rendered, err := runCLI(t, "render", "--db", db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}

	// The generated-at line differs by the second, so compare the blocks.
	for _, want := range []string{"primary oncall", "Split the pool config", "CDSS-1744"} {
		if !strings.Contains(body, want) {
			t.Errorf("the served page does not contain %q:\n%s", want, body)
		}
		if !strings.Contains(rendered, want) {
			t.Errorf("`render` does not contain %q, so the comparison is empty", want)
		}
	}
	if strings.Contains(body, "Roll the change out") {
		t.Errorf("the served page shows a blocked action:\n%s", body)
	}
}

// TestServeReflectsChanges: the page is built per request, so the next reload
// shows what just happened.
func TestServeReflectsChanges(t *testing.T) {
	db := initDB(t)
	withFetcher(t, stubFetcher{result: github.Result{}})
	base := serving(t, db, "--no-sync", "--no-watch")

	if body := fetch(t, base+"/"); !strings.Contains(body, "nothing to do") {
		t.Fatalf("the empty page does not say so:\n%s", body)
	}

	addAction(t, db, "--title", "something new", "--verb", "write")

	if body := fetch(t, base+"/"); !strings.Contains(body, "something new") {
		t.Errorf("the page did not pick up a new action:\n%s", body)
	}
}

// TestServeStopsOnInterrupt: all three services share a process, and the
// command must return rather than hang when its context ends.
func TestServeStopsOnInterrupt(t *testing.T) {
	db := initDB(t)
	withFetcher(t, stubFetcher{result: github.Result{}})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServeCLI(ctx, t, db, "--addr", "127.0.0.1:0", "--no-sync", "--no-watch") }()

	// Give it a moment to start, then stop it.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil && !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("serve returned %v, want a clean stop", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestServeRejectsANonPositiveInterval(t *testing.T) {
	db := initDB(t)
	_, err := runCLI(t, "serve", "--db", db, "--interval", "0")
	if err == nil {
		t.Fatal("serve accepted --interval 0, want an error")
	}
	if !strings.Contains(err.Error(), "must be positive") {
		t.Errorf("error = %v", err)
	}
}

// serving starts `roz serve` on a port the operating system picks and
// returns its base URL, stopping it when the test ends.
func serving(t *testing.T, db string, extra ...string) string {
	t.Helper()

	// A listener of our own would race the command for the port, so the
	// address is read back out of the log line the server prints.
	log := &syncedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	args := append([]string{"serve", "--db", db, "--addr", "127.0.0.1:0"}, extra...)
	go func() { done <- runCLIContext(ctx, log, args...) }()

	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("serve did not stop")
		}
	})

	base := waitForAddress(t, log)
	return base
}

func waitForAddress(t *testing.T, log *syncedBuffer) string {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, after, found := strings.Cut(log.String(), "serving "); found {
			if url, _, ok := strings.Cut(after, "\n"); ok {
				return strings.TrimSuffix(strings.TrimSpace(url), "/")
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the server never said where it was listening: %q", log.String())
	return ""
}

func fetch(t *testing.T, url string) string {
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
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s returned %d: %s", url, resp.StatusCode, body)
	}
	return string(body)
}

func runServeCLI(ctx context.Context, t *testing.T, db string, extra ...string) error {
	t.Helper()
	return runCLIContext(ctx, io.Discard, append([]string{"serve", "--db", db}, extra...)...)
}

// runCLIContext is runCLI with a context, so a long-running command can be
// stopped, and with somewhere to put the output while it runs.
func runCLIContext(ctx context.Context, out io.Writer, args ...string) error {
	root := NewRootCmd()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

// syncedBuffer collects output written by the serve goroutine and read by
// the test.
type syncedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestServedPageIsLive: the page the server hands out knows to listen; the
// one written to a file has nothing to listen to.
func TestServedPageIsLive(t *testing.T) {
	db := initDB(t)
	withFetcher(t, stubFetcher{result: github.Result{}})
	base := serving(t, db, "--no-sync", "--no-watch")

	served := fetch(t, base+"/")
	if !strings.Contains(served, "EventSource") {
		t.Errorf("the served page does not listen for changes:\n%s", served)
	}

	written, err := runCLI(t, "render", "--db", db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if strings.Contains(written, "EventSource") {
		t.Errorf("a page written to a file listens to a server that is not there:\n%s", written)
	}
}

// TestServeStreamsWhenTheLogMoves is the whole of SL16 end to end: something
// happens, and a page left open is told.
func TestServeStreamsWhenTheLogMoves(t *testing.T) {
	db := initDB(t)
	withFetcher(t, stubFetcher{result: github.Result{}})
	base := serving(t, db, "--no-sync", "--no-watch")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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

	arrived := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				arrived <- scanner.Text()
				return
			}
		}
		close(arrived)
	}()

	// Anything at all: every change writes an event.
	addAction(t, db, "--title", "something happened", "--verb", "write")

	select {
	case got, ok := <-arrived:
		if !ok {
			t.Fatal("the stream ended without telling us anything")
		}
		if !strings.HasPrefix(got, "data: ") {
			t.Errorf("event = %q", got)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the log moved and the stream said nothing")
	}
}

// TestServeStopsWhenTheSchemaMoves is the wiring test. The guard is a service
// like any other, so its failure should take the server down with it and be
// what the command reports — rather than the whole thing carrying on until
// some query hits a column that no longer exists.
//
// It waits out the real check interval rather than reaching past it, since the
// interval being usable is part of what is being claimed.
func TestServeStopsWhenTheSchemaMoves(t *testing.T) {
	db := initDB(t)
	withFetcher(t, stubFetcher{result: github.Result{}})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Wait for the address line rather than for a duration. The guard takes
	// its baseline as the services start, and migrating before that would put
	// the migration *into* the baseline — the test would then hang rather than
	// fail, which is a poor way to find out.
	log := &syncedBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- runCLIContext(ctx, log,
			"serve", "--db", db, "--addr", "127.0.0.1:0", "--no-sync", "--no-watch")
	}()
	waitForAddress(t, log)

	migrateUnderneath(t, db)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("serve returned nil, want it to stop over the migration")
		}
		if !strings.Contains(err.Error(), "migrated") {
			t.Errorf("error = %v, want it to say the database migrated", err)
		}
		// The service runner names whichever service failed, which is how an
		// operator tells this apart from the server or the syncer giving up.
		if !strings.Contains(err.Error(), "schema:") {
			t.Errorf("error = %v, want the failing service named", err)
		}
	case <-ctx.Done():
		t.Fatal("serve did not stop after the database migrated under it")
	}
}
