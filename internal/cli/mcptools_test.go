package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/scottlaird/todo/internal/mcp"
)

func tools(t *testing.T, db string) *mcpTools {
	t.Helper()
	return &mcpTools{db: db, agent: "mcp"}
}

func call(t *testing.T, m *mcpTools, client, name string, arguments map[string]any) string {
	t.Helper()

	out, err := m.Call(mcp.WithClient(context.Background(), client), name, arguments)
	if err != nil {
		t.Fatalf("%s(%v) returned error: %v", name, arguments, err)
	}
	return out
}

// TestToolsAreTheCommands is the property the whole design rests on: the two
// surfaces cannot drift, because there is only one of them.
func TestToolsAreTheCommands(t *testing.T) {
	listed := map[string]bool{}
	for _, tool := range tools(t, initDB(t)).List() {
		listed[tool.Name] = true
	}

	for _, want := range []string{
		"project_add", "project_close", "action_add", "action_close",
		"action_add-blocker", "pr_track", "pr_announce", "repo_track",
		"note", "exception", "sync", "render", "verb_list", "pipeline_list",
	} {
		if !listed[want] {
			t.Errorf("%s is a command but not a tool", want)
		}
	}

	// The ones an agent has no business calling, and cobra's own.
	for _, unwanted := range []string{"init", "serve", "syncer", "watch", "mcp", "completion", "help"} {
		if listed[unwanted] {
			t.Errorf("%s is exposed as a tool", unwanted)
		}
	}
}

// TestSchemaComesFromTheFlags: a flag added to a command is an argument
// without anyone writing it down twice.
func TestSchemaComesFromTheFlags(t *testing.T) {
	var add mcp.Tool
	for _, tool := range tools(t, initDB(t)).List() {
		if tool.Name == "action_add" {
			add = tool
		}
	}
	if add.Name == "" {
		t.Fatal("action_add is missing")
	}

	properties := add.InputSchema["properties"].(map[string]any)
	for _, want := range []string{"title", "verb", "project", "why", "rank-pin"} {
		if _, ok := properties[want]; !ok {
			t.Errorf("the schema has no %q", want)
		}
	}
	if got := properties["rank-pin"].(map[string]any)["type"]; got != "integer" {
		t.Errorf("rank-pin is %v, want integer", got)
	}

	// The two the server decides are not the caller's to set.
	for _, hidden := range []string{"db", "actor"} {
		if _, ok := properties[hidden]; ok {
			t.Errorf("%q is offered to the caller", hidden)
		}
	}

	required := add.InputSchema["required"].([]string)
	if !contains(required, "title") || !contains(required, "verb") {
		t.Errorf("required = %v, want title and verb", required)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestPositionalsComeFromTheUseLine: cobra records them nowhere else.
func TestPositionalsComeFromTheUseLine(t *testing.T) {
	all := tools(t, initDB(t)).List()
	byName := map[string]mcp.Tool{}
	for _, tool := range all {
		byName[tool.Name] = tool
	}

	show := byName["project_show"].InputSchema
	if _, ok := show["properties"].(map[string]any)["project"]; !ok {
		t.Errorf("project_show takes no project: %v", show)
	}
	if !contains(show["required"].([]string), "project") {
		t.Errorf("project is not required: %v", show)
	}

	// `note <subject> <text>` takes two, in order.
	note := byName["note"].InputSchema["properties"].(map[string]any)
	for _, want := range []string{"subject", "text"} {
		if _, ok := note[want]; !ok {
			t.Errorf("note takes no %q: %v", want, note)
		}
	}
}

func TestCallRunsTheCommand(t *testing.T) {
	db := initDB(t)
	m := tools(t, db)

	if got := call(t, m, "claude-code", "project_add", map[string]any{
		"title": "from the agent", "priority": float64(2),
	}); got != "TD1" {
		t.Errorf("project_add printed %q, want TD1", got)
	}

	// A number arrives as a float and must not reach the command line as 2e+00.
	shown := call(t, m, "claude-code", "project_show", map[string]any{"project": "TD1"})
	if !strings.Contains(shown, "priority") || !strings.Contains(shown, "2") {
		t.Errorf("project_show = %q", shown)
	}
}

// TestWritesAreAttributedToTheCaller is why the client's name is carried
// through the protocol at all.
func TestWritesAreAttributedToTheCaller(t *testing.T) {
	db := initDB(t)
	m := tools(t, db)
	call(t, m, "Claude Code", "project_add", map[string]any{"title": "from the agent"})

	out, err := runCLI(t, "watch", "--db", db, "--once", "--since", "2000-01-01")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if !strings.Contains(out, "agent:claude-code") {
		t.Errorf("the log does not name the caller:\n%s", out)
	}
	if strings.Contains(out, "  human ") {
		t.Errorf("an agent's change was recorded as a person's:\n%s", out)
	}
}

// TestAnUnnamedClientStillGetsAnAgentActor: no identity is not a reason to
// write as a person.
func TestAnUnnamedClientStillGetsAnAgentActor(t *testing.T) {
	db := initDB(t)
	m := tools(t, db)
	call(t, m, "", "project_add", map[string]any{"title": "anonymous"})

	out, err := runCLI(t, "watch", "--db", db, "--once", "--since", "2000-01-01")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if !strings.Contains(out, "agent:mcp") {
		t.Errorf("the fallback actor is missing:\n%s", out)
	}
}

func TestCallRejections(t *testing.T) {
	db := initDB(t)
	m := tools(t, db)
	call(t, m, "claude-code", "project_add", map[string]any{"title": "one"})

	tests := []struct {
		name      string
		tool      string
		arguments map[string]any
		wantErr   string
	}{
		{"unknown tool", "nonesuch", nil, "no tool called"},
		{"unknown argument", "project_add",
			map[string]any{"title": "x", "nonsense": "y"}, "takes no argument"},
		{"choosing the actor", "project_add",
			map[string]any{"title": "x", "actor": "human"}, `takes no argument called "actor"`},
		{"choosing the database", "project_add",
			map[string]any{"title": "x", "db": "/tmp/elsewhere.db"}, `takes no argument called "db"`},
		{"a missing positional", "project_show", nil, `needs "project"`},
		{"what the command itself refuses", "action_add",
			map[string]any{"title": "x", "verb": "teleport"}, "is not a verb"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := m.Call(context.Background(), tt.tool, tt.arguments)
			if err == nil {
				t.Fatalf("%s(%v) returned nil, want an error", tt.tool, tt.arguments)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestBooleanFlagsArePresenceNotValue: --unblocked takes no value, and
// passing false must leave it off rather than pass "false".
func TestBooleanFlagsArePresenceNotValue(t *testing.T) {
	db := initDB(t)
	m := tools(t, db)

	ready := call(t, m, "claude-code", "action_add",
		map[string]any{"title": "ready work", "verb": "write"})
	blocked := call(t, m, "claude-code", "action_add",
		map[string]any{"title": "blocked work", "verb": "run"})
	call(t, m, "claude-code", "action_add-blocker", map[string]any{"from": blocked, "to": ready})

	on := call(t, m, "claude-code", "action_list", map[string]any{"unblocked": true})
	if !strings.Contains(on, "ready work") || strings.Contains(on, "blocked work") {
		t.Errorf("unblocked=true did not filter:\n%s", on)
	}

	off := call(t, m, "claude-code", "action_list", map[string]any{"unblocked": false})
	if !strings.Contains(off, "blocked work") {
		t.Errorf("unblocked=false filtered anyway:\n%s", off)
	}
}

// TestRepeatedFlags: --design-ref is a stringArray, and an agent should be
// able to pass more than one.
func TestRepeatedFlags(t *testing.T) {
	db := initDB(t)
	m := tools(t, db)

	id := call(t, m, "claude-code", "project_add", map[string]any{
		"title":      "with refs",
		"design-ref": []any{"docs/a.md", "docs/b.md"},
	})
	shown := call(t, m, "claude-code", "project_show", map[string]any{"project": id})
	for _, want := range []string{"docs/a.md", "docs/b.md"} {
		if !strings.Contains(shown, want) {
			t.Errorf("%s is missing from the record:\n%s", want, shown)
		}
	}
}
