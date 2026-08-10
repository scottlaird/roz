package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scottlaird/todo/internal/github"
)

// TestServeServesTheRenderedPage is the join: what the server hands out is
// what `todo render` writes, because it is the same function.
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

// serving starts `todo serve` on a port the operating system picks and
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
