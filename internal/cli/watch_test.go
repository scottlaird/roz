package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// seedLog creates two projects so the log has something in it.
func seedLog(t *testing.T, db string) {
	t.Helper()
	for _, title := range []string{"one", "two"} {
		if _, err := runCLI(t, "project", "add", "--db", db, "--title", title); err != nil {
			t.Fatalf("project add returned error: %v", err)
		}
	}
}

func TestWatchOncePrintsBacklog(t *testing.T) {
	db := initDB(t)
	seedLog(t, db)

	out, err := runCLI(t, "watch", "--db", db, "--once")
	if err != nil {
		t.Fatalf("watch --once returned error: %v", err)
	}

	lines := nonEmptyLines(out)
	if len(lines) != 2 {
		t.Fatalf("watch --once printed %d lines, want 2:\n%s", len(lines), out)
	}
	for _, want := range []string{"ROZ1", "created", "human"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("first line does not contain %q: %s", want, lines[0])
		}
	}
}

// TestWatchOrdersOldestFirst pins the property the tail depends on: -n selects
// the newest events but prints them in the order they happened.
func TestWatchOrdersOldestFirst(t *testing.T) {
	db := initDB(t)
	seedLog(t, db)

	out, err := runCLI(t, "watch", "--db", db, "--once", "-n", "2")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}

	lines := nonEmptyLines(out)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "ROZ1") || !strings.Contains(lines[1], "ROZ2") {
		t.Errorf("events are not oldest first:\n%s", out)
	}
}

func TestWatchLinesZeroStartsAtTheEnd(t *testing.T) {
	db := initDB(t)
	seedLog(t, db)

	out, err := runCLI(t, "watch", "--db", db, "--once", "-n", "0")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if got := nonEmptyLines(out); len(got) != 0 {
		t.Errorf("-n 0 printed %d lines, want none:\n%s", len(got), out)
	}
}

func TestWatchJSONIsOnePerLine(t *testing.T) {
	db := initDB(t)
	seedLog(t, db)

	out, err := runCLI(t, "watch", "--db", db, "--once", "-o", "json")
	if err != nil {
		t.Fatalf("watch -o json returned error: %v", err)
	}

	lines := nonEmptyLines(out)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), out)
	}
	// Each line must parse on its own — that is what makes it streamable.
	for i, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line %d is not valid JSON on its own: %v\n%s", i, err, line)
		}
		if event["kind"] != "created" {
			t.Errorf("line %d kind = %#v, want created", i, event["kind"])
		}
	}
}

func TestWatchFilters(t *testing.T) {
	db := initDB(t)
	seedLog(t, db)

	tests := []struct {
		name  string
		args  []string
		lines int
	}{
		{name: "no filter", args: nil, lines: 2},
		{name: "matching kind", args: []string{"--kind", "created"}, lines: 2},
		{name: "other kind", args: []string{"--kind", "changed"}, lines: 0},
		{name: "matching severity", args: []string{"--severity", "info"}, lines: 2},
		{name: "other severity", args: []string{"--severity", "exception"}, lines: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"watch", "--db", db, "--once"}, tt.args...)
			out, err := runCLI(t, args...)
			if err != nil {
				t.Fatalf("watch returned error: %v", err)
			}
			if got := len(nonEmptyLines(out)); got != tt.lines {
				t.Errorf("got %d lines, want %d:\n%s", got, tt.lines, out)
			}
		})
	}
}

// TestWatchExcludesAnActor is the point of the flag: an agent hides its own
// writes and keeps everything else, which an allow-list cannot express — the
// actor vocabulary is open, so "everyone but me" has no finite spelling.
func TestWatchExcludesAnActor(t *testing.T) {
	db := initDB(t)
	if _, err := runCLI(t, "project", "add", "--db", db, "--title", "mine",
		"--actor", "agent:claude"); err != nil {
		t.Fatalf("project add returned error: %v", err)
	}
	if _, err := runCLI(t, "project", "add", "--db", db, "--title", "theirs",
		"--actor", "agent:other"); err != nil {
		t.Fatalf("project add returned error: %v", err)
	}
	seedLog(t, db) // two more, as human

	tests := []struct {
		name  string
		args  []string
		want  []string
		lines int
	}{
		{
			name:  "one actor",
			args:  []string{"--exclude-actor", "agent:claude"},
			want:  []string{"agent:other", "human"},
			lines: 3,
		},
		{
			name:  "comma separated",
			args:  []string{"--exclude-actor", "agent:claude,agent:other"},
			want:  []string{"human"},
			lines: 2,
		},
		{
			name:  "repeated",
			args:  []string{"--exclude-actor", "agent:claude", "--exclude-actor", "human"},
			want:  []string{"agent:other"},
			lines: 1,
		},
		{
			name:  "an actor nobody wrote as",
			args:  []string{"--exclude-actor", "sync:github"},
			want:  []string{"agent:claude", "agent:other", "human"},
			lines: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"watch", "--db", db, "--once", "-n", "10"}, tt.args...)
			out, err := runCLI(t, args...)
			if err != nil {
				t.Fatalf("watch returned error: %v", err)
			}
			if got := len(nonEmptyLines(out)); got != tt.lines {
				t.Errorf("got %d lines, want %d:\n%s", got, tt.lines, out)
			}
			for _, actor := range tt.want {
				if !strings.Contains(out, actor) {
					t.Errorf("%s was excluded too:\n%s", actor, out)
				}
			}
			for _, excluded := range tt.args[1:] {
				for _, actor := range strings.Split(excluded, ",") {
					if strings.Contains(out, actor+" ") {
						t.Errorf("%s was not excluded:\n%s", actor, out)
					}
				}
			}
		})
	}
}

// TestWatchExcludesAnActorInJSON: the filter is in the query, so it cannot
// depend on which printer runs — but the JSON form is what an agent reads,
// and it is the form the jq workaround this replaces operated on.
func TestWatchExcludesAnActorInJSON(t *testing.T) {
	db := initDB(t)
	if _, err := runCLI(t, "project", "add", "--db", db, "--title", "mine",
		"--actor", "agent:claude"); err != nil {
		t.Fatalf("project add returned error: %v", err)
	}
	seedLog(t, db)

	out, err := runCLI(t, "watch", "--db", db, "--once", "-n", "10",
		"-o", "json", "--exclude-actor", "agent:claude")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}

	lines := nonEmptyLines(out)
	if len(lines) != 2 {
		t.Fatalf("got %d events, want 2:\n%s", len(lines), out)
	}
	for _, line := range lines {
		var event struct {
			Actor string `json:"actor"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("watch -o json printed %q: %v", line, err)
		}
		if event.Actor == "agent:claude" {
			t.Errorf("an excluded actor survived into the JSON: %s", line)
		}
	}
}

// TestWatchExcludesWhileFollowing. The backlog bounds are reset for the tail
// and the filters are not; an exclusion that only held for the backlog would
// be worse than none, since the noise would come back the moment it mattered.
func TestWatchExcludesWhileFollowing(t *testing.T) {
	db := initDB(t)

	root := NewRootCmd()
	out := &syncBuffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"watch", "--db", db, "--since", "2099-01-01",
		"--interval", "20ms", "--exclude-actor", "agent:claude"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	time.Sleep(100 * time.Millisecond)

	if _, err := runCLI(t, "project", "add", "--db", db, "--title", "mine",
		"--actor", "agent:claude"); err != nil {
		t.Fatalf("project add returned error: %v", err)
	}
	if _, err := runCLI(t, "project", "add", "--db", db, "--title", "theirs"); err != nil {
		t.Fatalf("project add returned error: %v", err)
	}

	if err := waitFor(func() bool { return strings.Contains(out.String(), "ROZ2") }); err != nil {
		t.Fatalf("watch did not report the event it was meant to keep: %v\n%s", err, out.String())
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("watch returned error: %v", err)
	}

	if strings.Contains(out.String(), "agent:claude") {
		t.Errorf("the excluded actor came back once the tail started:\n%s", out.String())
	}
	if lines := nonEmptyLines(out.String()); len(lines) != 1 {
		t.Errorf("watch printed %d lines, want only the kept event:\n%s", len(lines), out.String())
	}
}

func TestWatchSince(t *testing.T) {
	db := initDB(t)
	seedLog(t, db)

	out, err := runCLI(t, "watch", "--db", db, "--once", "--since", "2000-01-01")
	if err != nil {
		t.Fatalf("watch --since returned error: %v", err)
	}
	if got := len(nonEmptyLines(out)); got != 2 {
		t.Errorf("--since in the past printed %d lines, want 2", got)
	}

	out, err = runCLI(t, "watch", "--db", db, "--once", "--since", "2099-01-01")
	if err != nil {
		t.Fatalf("watch --since returned error: %v", err)
	}
	if got := len(nonEmptyLines(out)); got != 0 {
		t.Errorf("--since in the future printed %d lines, want none", got)
	}
}

func TestWatchRejections(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "unparseable since",
			args:    []string{"--since", "yesterday"},
			wantErr: "not a date or timestamp",
		},
		{
			name:    "unknown severity",
			args:    []string{"--severity", "urgent"},
			wantErr: "not recognised",
		},
		{
			name:    "zero interval",
			args:    []string{"--interval", "0s"},
			wantErr: "must be positive",
		},
		{
			name:    "negative lines",
			args:    []string{"-n", "-1"},
			wantErr: "must not be negative",
		},
		{
			name:    "empty exclusion",
			args:    []string{"--exclude-actor", ""},
			wantErr: "no name",
		},
		{
			name:    "empty name among others",
			args:    []string{"--exclude-actor", "agent:claude,,human"},
			wantErr: "empty name",
		},
		{
			name:    "since with lines",
			args:    []string{"--since", "2000-01-01", "-n", "5"},
			wantErr: "if any flags in the group",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := initDB(t)
			args := append([]string{"watch", "--db", db, "--once"}, tt.args...)

			_, err := runCLI(t, args...)
			if err == nil {
				t.Fatalf("watch %v returned nil, want an error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestWatchFollowsNewEvents runs the real follow loop: it starts watching,
// writes an event, and expects only that event to appear.
//
// It also covers the bug the cursor logic exists for — a backlog that matches
// nothing must not cause the first poll to replay the whole log.
func TestWatchFollowsNewEvents(t *testing.T) {
	db := initDB(t)
	seedLog(t, db)

	root := NewRootCmd()
	out := &syncBuffer{}
	root.SetOut(out)
	root.SetErr(out)
	// --since in the future selects no backlog, so anything printed can only
	// have come from following.
	root.SetArgs([]string{"watch", "--db", db, "--since", "2099-01-01", "--interval", "20ms"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	// Let the watcher take its starting cursor before anything new lands.
	time.Sleep(100 * time.Millisecond)
	if got := out.String(); got != "" {
		t.Fatalf("watch printed before any new event, want nothing:\n%s", got)
	}

	if _, err := runCLI(t, "project", "add", "--db", db, "--title", "appeared later"); err != nil {
		t.Fatalf("project add returned error: %v", err)
	}

	if err := waitFor(func() bool { return strings.Contains(out.String(), "ROZ3") }); err != nil {
		t.Fatalf("watch did not report the new event: %v\n%s", err, out.String())
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("watch returned error: %v", err)
	}

	// Exactly the new event, not a replay of the two seeded ones.
	if lines := nonEmptyLines(out.String()); len(lines) != 1 {
		t.Errorf("watch printed %d lines, want only the new event:\n%s", len(lines), out.String())
	}
}

func waitFor(condition func() bool) error {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("timed out")
}

// syncBuffer is a bytes.Buffer safe for the test to read while the watch
// goroutine writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func nonEmptyLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestWatchFilterAsksWhatTheFlagsCannot. The three flags are each a special
// case of one expression, and the questions worth asking are mostly not
// single-column equalities.
func TestWatchFilterAsksWhatTheFlagsCannot(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "the subject")
	if _, err := runCLI(t, "project", "set", "--db", db, project, "--priority", "2"); err != nil {
		t.Fatalf("project set returned error: %v", err)
	}
	if _, err := runCLI(t, "project", "set", "--db", db, project, "--title", "renamed"); err != nil {
		t.Fatalf("project set returned error: %v", err)
	}

	tests := []struct {
		name   string
		filter string
		lines  int
	}{
		{name: "one field", filter: `field == "priority"`, lines: 1},
		{name: "a subject prefix", filter: `subject_id.startsWith("` + project[:2] + `")`, lines: 3},
		{name: "two columns", filter: `kind == "changed" && field == "title"`, lines: 1},
		{name: "matches nothing", filter: `actor == "sync:github"`, lines: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runCLI(t, "watch", "--db", db, "--once", "-n", "20", "--filter", tt.filter)
			if err != nil {
				t.Fatalf("watch --filter %q returned error: %v", tt.filter, err)
			}
			if got := len(nonEmptyLines(out)); got != tt.lines {
				t.Errorf("got %d lines, want %d:\n%s", got, tt.lines, out)
			}
		})
	}
}

// TestWatchFilterRunsInTheQuery. It matters more on the log than on a listing:
// a tail re-runs its query every interval, so a predicate left to Go reads
// every new row for ever rather than once.
func TestWatchFilterRunsInTheQuery(t *testing.T) {
	db := initDB(t)
	seedLog(t, db)

	out, err := runCLI(t, "watch", "--db", db, "--once", "-n", "20",
		"--filter", `kind == "created"`, "--explain-filter")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if !strings.Contains(out, "filter ran in SQL") {
		t.Errorf("the filter did not reach the query:\n%s", out)
	}

	// payload is a JSON column, which cel2sql cannot convert (#202). Falling
	// back to Go is the right answer; generating SQL that does not run is not.
	out, err = runCLI(t, "watch", "--db", db, "--once", "-n", "20",
		"--filter", `payload == ""`, "--explain-filter")
	if err != nil {
		t.Fatalf("watch on a JSON column returned error: %v", err)
	}
	if !strings.Contains(out, "ran in Go") {
		t.Errorf("a JSON column did not fall back to Go:\n%s", out)
	}
}

// TestWatchFilterComposesWithTheFlags: --kind narrows and --filter narrows
// again, rather than one quietly replacing the other.
func TestWatchFilterComposesWithTheFlags(t *testing.T) {
	db := initDB(t)
	project := addProject(t, db, "the subject")
	if _, err := runCLI(t, "project", "set", "--db", db, project,
		"--priority", "2", "--actor", "agent:claude"); err != nil {
		t.Fatalf("project set returned error: %v", err)
	}

	out, err := runCLI(t, "watch", "--db", db, "--once", "-n", "20",
		"--kind", "changed", "--filter", `actor == "agent:claude"`)
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if got := len(nonEmptyLines(out)); got != 1 {
		t.Errorf("got %d lines, want the one event both narrow to:\n%s", got, out)
	}

	// Each on its own keeps more, which is what makes the above a conjunction
	// rather than one flag winning.
	for _, args := range [][]string{
		{"--kind", "changed"},
		{"--filter", `actor == "agent:claude"`},
	} {
		out, err := runCLI(t, append([]string{"watch", "--db", db, "--once", "-n", "20"}, args...)...)
		if err != nil {
			t.Fatalf("watch %v returned error: %v", args, err)
		}
		if len(nonEmptyLines(out)) < 1 {
			t.Errorf("watch %v found nothing at all:\n%s", args, out)
		}
	}
}

// TestWatchTakesASavedView is the point of registering the log as an entity:
// an agent's exclusion is a property of the agent, not of the invocation.
func TestWatchTakesASavedView(t *testing.T) {
	db := initDB(t)
	if _, err := runCLI(t, "project", "add", "--db", db, "--title", "mine",
		"--actor", "agent:claude"); err != nil {
		t.Fatalf("project add returned error: %v", err)
	}
	seedLog(t, db)

	if _, err := runCLI(t, "view", "add", "notmine", "--db", db,
		"--entity", "event", "--filter", `actor != "agent:claude"`); err != nil {
		t.Fatalf("view add returned error: %v", err)
	}

	out, err := runCLI(t, "watch", "--db", db, "--once", "-n", "20", "--view", "notmine")
	if err != nil {
		t.Fatalf("watch --view returned error: %v", err)
	}
	if strings.Contains(out, "agent:claude") {
		t.Errorf("the view did not exclude the agent's own writes:\n%s", out)
	}
	// Three, not two: saving the view is itself a change to the database and
	// is in the log like anything else, written by the human who saved it.
	if got := len(nonEmptyLines(out)); got != 3 {
		t.Errorf("got %d lines, want the two projects and the view's own creation:\n%s", got, out)
	}

	// A view of another listing names columns this one does not have, and the
	// error should say that rather than "no such column".
	_, err = runCLI(t, "watch", "--db", db, "--once", "--view", "open_actions")
	if err == nil || !strings.Contains(err.Error(), "is of action") {
		t.Errorf("watch --view open_actions returned %v, want a wrong-entity error", err)
	}
}

// TestWatchFilterAdvancesTheCursorOverWhatItHides. The tail's cursor is the
// last row read, not the last row printed: a filter that hides an event must
// not make the next poll read it again for ever.
func TestWatchFilterAdvancesTheCursorOverWhatItHides(t *testing.T) {
	db := initDB(t)

	root := NewRootCmd()
	out := &syncBuffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"watch", "--db", db, "--since", "2099-01-01",
		"--interval", "20ms", "--filter", `kind == "note"`})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	time.Sleep(100 * time.Millisecond)

	// Hidden by the filter, and read by the tail.
	project := addProject(t, db, "not a note")
	if _, err := runCLI(t, "note", "--db", db, project, "this one shows"); err != nil {
		t.Fatalf("note returned error: %v", err)
	}

	if err := waitFor(func() bool { return strings.Contains(out.String(), "this one shows") }); err != nil {
		t.Fatalf("watch did not report the matching event: %v\n%s", err, out.String())
	}
	// Long enough for several more polls, which would replay the hidden event
	// if the cursor had stopped at the last printed line.
	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Errorf("watch returned error: %v", err)
	}

	if lines := nonEmptyLines(out.String()); len(lines) != 1 {
		t.Errorf("watch printed %d lines, want only the matching one:\n%s", len(lines), out.String())
	}
}
