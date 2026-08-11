package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/scottlaird/todo/internal/store"
)

// schemaCheckInterval is how often a long-running command asks whether the
// database has migrated under it.
//
// The check is two integers out of a table with a row per migration, so the
// cost is not the question. How long a process should keep working against a
// schema it no longer understands is, and a few seconds is short enough that
// nothing much happens in it.
const schemaCheckInterval = 5 * time.Second

// schemaGuard stops a long-running command when the database migrates
// underneath it.
//
// `serve`, `syncer`, `watch` and `mcp` read the schema once, at startup:
// OpenStore checks it there and every query afterwards assumes it. An upgrade
// applied by another process — a rebuilt binary, a second checkout — leaves
// them querying columns that no longer exist, and the failure surfaces as
// whatever query happens to run first rather than as "your server is out of
// date". Migration 0008 dropped five columns from `project`, which is exactly
// the shape that breaks a server left running from the morning.
//
// Exiting rather than reloading, on the grounds that a restart is cheap and a
// process serving a schema it does not understand is not. `mcp` is respawned
// by its client, and `serve` already stops its other services when one fails.
type schemaGuard struct {
	store *store.Store
	// interval overrides schemaCheckInterval, for tests.
	interval time.Duration
	// ready, when set, is closed once the baseline has been taken.
	//
	// Only a test needs it, and it needs it for a real reason: "has the schema
	// moved since startup" has no answer before startup has been observed, so
	// a test that migrates the database first would be migrating it into the
	// baseline and waiting forever. server.Server carries the same channel for
	// the same shape of problem.
	ready chan struct{}
}

func (g *schemaGuard) Name() string { return "schema" }

func (g *schemaGuard) Run(ctx context.Context) error {
	interval := g.interval
	if interval <= 0 {
		interval = schemaCheckInterval
	}

	// The baseline is what OpenStore already approved, so this is not a second
	// opinion about whether the schema is right — only about whether it moved.
	started, err := g.store.SchemaState(ctx)
	if err != nil {
		// Cancellation can land inside this read, when another service finishes
		// as this one starts — `todo mcp` against a client that hung up
		// immediately. A service must return nil for that, or a clean shutdown
		// is reported as a failure. Same treatment the loop below gives.
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if g.ready != nil {
		close(g.ready)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			current, err := g.store.SchemaState(ctx)
			if err != nil {
				// A busy database is not a migration, and taking the process
				// down over one would be worse than the thing this prevents.
				// The next tick asks again.
				if ctx.Err() != nil {
					return nil
				}
				continue
			}
			if current != started {
				return fmt.Errorf(
					"the database migrated while this was running: now %s, was %s; "+
						"restart to pick up the new schema", current, started)
			}
		}
	}
}
