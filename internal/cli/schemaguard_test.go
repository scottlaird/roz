package cli

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/scottlaird/roz/internal/mcp"
	"github.com/scottlaird/roz/internal/service"
	"github.com/scottlaird/roz/internal/store"
)

// openTestStore opens a second handle on a database the CLI helpers built, so
// a test can hold one the way a long-running command does.
func openTestStore(t *testing.T, db string) *store.Store {
	t.Helper()
	st, err := store.OpenStore(context.Background(), db)
	if err != nil {
		t.Fatalf("OpenStore() returned error: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// migrateUnderneath stands in for another process applying a migration: the
// guard reads the record rather than the schema, so a row is the whole of what
// it notices.
func migrateUnderneath(t *testing.T, db string) {
	t.Helper()
	sql, err := store.Open(db)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer sql.Close()

	_, err = sql.Exec(
		"INSERT INTO applied_migration (version, name, applied_at) VALUES (9999, '9999_later.sql', '')")
	if err != nil {
		t.Fatalf("recording a migration: %v", err)
	}
}

// startedGuard returns a guard that is already running and has taken its
// baseline, so a migration after this call is one it can still notice.
func startedGuard(t *testing.T, db string) (*schemaGuard, <-chan error, context.CancelFunc) {
	t.Helper()

	guard := &schemaGuard{
		store:    openTestStore(t, db),
		interval: 10 * time.Millisecond,
		ready:    make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- guard.Run(ctx) }()

	select {
	case <-guard.ready:
	case err := <-done:
		cancel()
		t.Fatalf("the guard stopped before taking a baseline: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("the guard never took a baseline")
	}
	return guard, done, cancel
}

func TestSchemaGuardStopsWhenTheSchemaMoves(t *testing.T) {
	db := initDB(t)
	_, done, cancel := startedGuard(t, db)
	defer cancel()

	migrateUnderneath(t, db)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the guard returned nil, want it to stop the process")
		}
		// The message is what the operator sees, and "restart" is the whole
		// of the advice.
		for _, want := range []string{"migrated", "restart"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to mention %q", err, want)
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the guard did not notice the migration")
	}
}

// TestSchemaGuardIsQuietWhileNothingMoves: it must return nil on cancellation,
// or it would take the services beside it down on every clean shutdown.
func TestSchemaGuardIsQuietWhileNothingMoves(t *testing.T) {
	db := initDB(t)
	guard := &schemaGuard{store: openTestStore(t, db), interval: 10 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- guard.Run(ctx) }()

	time.Sleep(50 * time.Millisecond) // several ticks with nothing happening
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the guard returned %v on a clean shutdown, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the guard did not stop when cancelled")
	}
}

// TestSchemaGuardIgnoresOrdinaryWrites: the log moves constantly, and a guard
// that mistook that for a migration would restart the world every second.
func TestSchemaGuardIgnoresOrdinaryWrites(t *testing.T) {
	db := initDB(t)
	_, done, cancel := startedGuard(t, db)
	defer cancel()

	addProject(t, db, "something to write about")
	addAction(t, db, "--title", "and an action", "--verb", "write")
	time.Sleep(50 * time.Millisecond)

	select {
	case err := <-done:
		t.Fatalf("the guard stopped over ordinary writes: %v", err)
	default:
	}
}

// silentClient is a stdin that never produces a line: a client that connected
// and then went quiet, which is what an MCP server spends most of its life
// looking at.
type silentClient struct{ closed chan struct{} }

func (c silentClient) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

// TestMCPStopsWhenTheSchemaMoves: Serve blocks reading stdin and cannot be
// interrupted, so without mcpService racing the context against it the whole
// command would sit here waiting for a request that may never come — with the
// guard having already decided it must not answer one.
func TestMCPStopsWhenTheSchemaMoves(t *testing.T) {
	db := initDB(t)
	quiet := silentClient{closed: make(chan struct{})}
	defer close(quiet.closed)

	server := &mcp.Server{
		Name:    "roz",
		Version: version,
		Tools:   &mcpTools{db: db, agent: "test"},
	}

	guard := &schemaGuard{
		store:    openTestStore(t, db),
		interval: 10 * time.Millisecond,
		ready:    make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- service.Run(ctx,
			&mcpService{server: server, in: quiet, out: io.Discard},
			guard)
	}()

	select {
	case <-guard.ready:
	case <-time.After(10 * time.Second):
		t.Fatal("the guard never took a baseline")
	}
	migrateUnderneath(t, db)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("mcp returned nil, want it to stop over the migration")
		}
		if !strings.Contains(err.Error(), "migrated") {
			t.Errorf("error = %v, want it to say the database migrated", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("mcp did not stop; it is still waiting for a request")
	}
}

// TestMCPStopsWhenItsClientGoesAway: the guard must not hold the process open
// after the protocol loop is done. `roz mcp` ends when its stdin does, and
// gaining a second service silently took that away until service.Run learned
// that a service finishing stops the rest.
func TestMCPStopsWhenItsClientGoesAway(t *testing.T) {
	db := initDB(t)

	server := &mcp.Server{
		Name:    "roz",
		Version: version,
		Tools:   &mcpTools{db: db, agent: "test"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		// An empty reader is a client that connected and hung up.
		done <- service.Run(ctx,
			&mcpService{server: server, in: strings.NewReader(""), out: io.Discard},
			&schemaGuard{store: openTestStore(t, db)})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("mcp returned %v, want nil: a client hanging up is not a failure", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("mcp did not exit after its stdin ended")
	}
}
