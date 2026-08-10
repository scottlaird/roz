package server

import (
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

	s := New("127.0.0.1:0", page, log)
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
	}, nil)

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
	s := New("127.0.0.1:0", nil, nil)
	if err := s.Run(context.Background()); err == nil {
		t.Error("Run() with no page returned nil, want an error")
	}
}

// TestAddressInUseIsReported: two `todo serve` processes should not leave the
// second one silently serving nothing.
func TestAddressInUseIsReported(t *testing.T) {
	page := func(context.Context) ([]byte, error) { return []byte("the page"), nil }
	base := running(t, page, nil)
	addr := strings.TrimPrefix(base, "http://")

	second := New(addr, page, nil)
	if err := second.Run(context.Background()); err == nil {
		t.Error("the second server started on a taken address, want an error")
	}
}
