package cli

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/scottlaird/roz/internal/mcp"
)

// pflagFlag is pflag.Flag under a shorter name, since it appears in every
// signature below.
type pflagFlag = pflag.Flag

// mcpTools exposes the command tree over MCP.
//
// The tools are derived from the cobra commands rather than written out
// again, and calling one runs that command. There is no second
// implementation to keep in step: a flag added to the CLI is a tool argument
// the next time the server starts, and a command that changes its mind about
// what it accepts changes the schema with it.
//
// What that cannot derive is which commands make sense here, which is a
// judgement — see skipped.
type mcpTools struct {
	// db is the database the server was started against, passed to every
	// command so a tool call cannot land somewhere else.
	db string
	// agent names the caller when it did not name itself.
	agent string
}

// skipped are the commands an agent has no business calling.
//
// init creates a database, which is a decision about where state lives, and
// db moves whole databases around for the same reason. serve and syncer never
// return. completion and help are cobra's.
var skipped = map[string]bool{
	"init":       true,
	"db":         true,
	"serve":      true,
	"syncer":     true,
	"completion": true,
	"help":       true,
	"mcp":        true,
}

// forced are flags the server always passes, keyed by tool.
//
// watch follows by default and would never return, so what is offered here is
// the bounded read: everything the command can filter and window, answered
// once. An agent asking "what has happened since sequence N" is the useful
// question, and it can ask again.
var forced = map[string][]string{"watch": {"--once"}}

// hiddenPerTool are flags one tool does not offer, because the server has
// already decided them. --interval only means something while following.
var hiddenPerTool = map[string]map[string]bool{
	"watch": {"once": true, "interval": true},
}

// hidden reports whether a flag is the server's rather than the caller's.
func hidden(tool, flag string) bool {
	return hiddenFlags[flag] || hiddenPerTool[tool][flag]
}

// hiddenFlags are handled by the server rather than offered to the caller.
// The database is fixed at startup, and the actor is derived from who is
// calling — letting an agent pick either would defeat the point of both.
var hiddenFlags = map[string]bool{
	"db":     true,
	"actor":  true,
	"help":   true,
	"output": true,
}

func (m *mcpTools) List() []mcp.Tool {
	var tools []mcp.Tool
	for _, cmd := range callableCommands(NewRootCmd()) {
		tools = append(tools, describeCommand(cmd))
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

// callableCommands walks the tree for the leaves worth exposing.
func callableCommands(root *cobra.Command) []*cobra.Command {
	var found []*cobra.Command
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, child := range cmd.Commands() {
			if skipped[child.Name()] || child.Hidden {
				continue
			}
			if child.HasSubCommands() {
				walk(child)
				continue
			}
			if child.Runnable() {
				found = append(found, child)
			}
		}
	}
	walk(root)
	return found
}

// toolName is the command path with the spaces turned into underscores:
// `roz action add` is action_add.
func toolName(cmd *cobra.Command) string {
	path := strings.Fields(cmd.CommandPath())
	return strings.Join(path[1:], "_") // drop "roz"
}

func describeCommand(cmd *cobra.Command) mcp.Tool {
	properties := map[string]any{}
	var required []string

	for _, arg := range positionalsOf(cmd) {
		properties[arg.name] = map[string]any{
			"type":        "string",
			"description": fmt.Sprintf("positional argument %d", arg.position+1),
		}
		if arg.required {
			required = append(required, arg.name)
		}
	}

	name := toolName(cmd)
	cmd.Flags().VisitAll(func(f *pflagFlag) {
		if hidden(name, f.Name) {
			return
		}
		properties[f.Name] = schemaForFlag(f)
		if isRequiredFlag(f) {
			required = append(required, f.Name)
		}
	})

	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = required
	}

	return mcp.Tool{
		Name:        name,
		Description: describe(name, cmd),
		InputSchema: schema,
	}
}

// describe is the command's own help, which is where the reasoning already
// lives. An agent reading it learns the same things a person does.
//
// Where the server has decided something for the caller, the help is
// describing a command they cannot actually invoke, so the difference is
// spelled out rather than left to be discovered — see constrained.
func describe(name string, cmd *cobra.Command) string {
	description := cmd.Short
	if cmd.Long != "" {
		description += "\n\n" + cmd.Long
	}
	if note, ok := constrained[name]; ok {
		description += "\n\n" + note
	}
	return description
}

// constrained is what the server has already settled, in the tool's own words.
//
// Only watch needs one so far, and it needs it badly: its help opens "Follow
// the event log" and goes on about the poll interval, none of which is true of
// a tool that answers once. An agent taking that at face value would either
// expect to be followed or never call it twice.
var constrained = map[string]string{
	"watch": "This tool is the bounded read, not the follow: the server forces " +
		"--once, so it returns the events matching the filters instead of " +
		"streaming. Nothing arrives between calls — ask again for what has " +
		"happened since, with --since, or -n for a count of recent events.",
}

func schemaForFlag(f *pflagFlag) map[string]any {
	schema := map[string]any{"description": f.Usage}
	switch f.Value.Type() {
	case "bool":
		schema["type"] = "boolean"
	case "int", "int64":
		schema["type"] = "integer"
	case "stringArray", "stringSlice":
		schema["type"] = "array"
		schema["items"] = map[string]any{"type": "string"}
	case "duration":
		schema["type"] = "string"
		schema["description"] = f.Usage + " (a duration, e.g. 60s)"
	default:
		schema["type"] = "string"
	}
	return schema
}

func isRequiredFlag(f *pflagFlag) bool {
	values, ok := f.Annotations[cobra.BashCompOneRequiredFlag]
	return ok && len(values) > 0 && values[0] == "true"
}

// positional is one argument named in a command's Use line.
type positional struct {
	name     string
	position int
	required bool
}

var positionalPattern = regexp.MustCompile(`[<\[]([^>\]]+)[>\]]`)

// positionalsOf reads the arguments out of the Use line, which is the only
// place cobra records that a command takes any: `show <project>` takes one
// called project, and `jira [issue-key]` takes an optional one.
func positionalsOf(cmd *cobra.Command) []positional {
	var args []positional
	for i, match := range positionalPattern.FindAllStringSubmatch(cmd.Use, -1) {
		raw := match[0]
		args = append(args, positional{
			name:     identifier(match[1]),
			position: i,
			required: strings.HasPrefix(raw, "<"),
		})
	}
	return args
}

var notIdentifier = regexp.MustCompile(`[^a-z0-9]+`)

// actorName turns what a client calls itself into an actor suffix. Hyphens
// rather than underscores, to read like sync:slack-manual does.
func actorName(raw string) string {
	return strings.Trim(notIdentifier.ReplaceAllString(strings.ToLower(raw), "-"), "-")
}

// identifier turns what the Use line says into something a JSON key can be:
// `owner/name` becomes owner_name.
func identifier(raw string) string {
	return strings.Trim(notIdentifier.ReplaceAllString(strings.ToLower(raw), "_"), "_")
}

// Call runs a tool by running the command it came from.
//
// Everything the CLI does — validation, the actor rule, the cascade, the
// wording of an error — happens here because it is the same code. The only
// things the server supplies are the database and the actor.
func (m *mcpTools) Call(ctx context.Context, name string, arguments map[string]any) (string, error) {
	cmd, ok := m.find(name)
	if !ok {
		return "", fmt.Errorf("no tool called %q", name)
	}

	argv, err := m.argv(ctx, cmd, arguments)
	if err != nil {
		return "", err
	}

	// A fresh tree per call: cobra keeps parsed values on the command, so a
	// reused one would carry the last call's flags into this one. Keeping
	// them there rather than in package variables is also what makes two
	// calls at once safe, which matters the moment this is served over
	// anything but stdio.
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(argv)

	if err := root.ExecuteContext(ctx); err != nil {
		// Cobra has usually printed something useful already; the error is
		// the summary, and both are worth returning.
		if out.Len() > 0 {
			return "", fmt.Errorf("%w\n%s", err, strings.TrimSpace(out.String()))
		}
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

func (m *mcpTools) find(name string) (*cobra.Command, bool) {
	for _, cmd := range callableCommands(NewRootCmd()) {
		if toolName(cmd) == name {
			return cmd, true
		}
	}
	return nil, false
}

// argv turns the call's arguments into a command line.
//
// Positionals go in the order the Use line names them; everything else is a
// flag. An argument the command does not have is refused rather than dropped,
// since silently ignoring it would look like it had been applied.
func (m *mcpTools) argv(ctx context.Context, cmd *cobra.Command, arguments map[string]any) ([]string, error) {
	name := toolName(cmd)
	path := strings.Fields(cmd.CommandPath())[1:]
	argv := append([]string{}, path...)

	known := map[string]bool{}
	for _, arg := range positionalsOf(cmd) {
		known[arg.name] = true
	}
	cmd.Flags().VisitAll(func(f *pflagFlag) {
		if !hidden(name, f.Name) {
			known[f.Name] = true
		}
	})
	for given := range arguments {
		if !known[given] {
			return nil, fmt.Errorf("%s takes no argument called %q", name, given)
		}
	}

	// Flags first, then positionals, so a value that looks like a flag
	// cannot be read as one. VisitAll is lexical, so the order is already
	// deterministic — and must not be sorted afterwards, which would
	// separate each flag from its value.
	var flags []string
	cmd.Flags().VisitAll(func(f *pflagFlag) {
		if hidden(name, f.Name) {
			return
		}
		value, ok := arguments[f.Name]
		if !ok {
			return
		}
		flags = append(flags, flagArgs(f, value)...)
	})
	argv = append(argv, flags...)

	argv = append(argv, m.fixedFlags(cmd, m.actor(ctx))...)
	argv = append(argv, forced[name]...)

	var positionals []string
	for _, arg := range positionalsOf(cmd) {
		value, ok := arguments[arg.name]
		if !ok {
			if arg.required {
				return nil, fmt.Errorf("%s needs %q", name, arg.name)
			}
			continue
		}
		positionals = append(positionals, asString(value))
	}
	return append(argv, positionals...), nil
}

// fixedFlags are what the server decides rather than the caller: which
// database, and who is asking.
func (m *mcpTools) fixedFlags(cmd *cobra.Command, actor string) []string {
	fixed := []string{"--db", m.db}
	if cmd.Flags().Lookup("actor") != nil {
		fixed = append(fixed, "--actor", actor)
	}
	return fixed
}

// actor is who the change gets recorded as.
//
// The protocol's clientInfo is the closest thing to an identity on offer, so
// a client that names itself is named in the log. It is always an agent:
// prefix — an agent writing as a person would make the history claim someone
// did something they did not, and the store would refuse a sync: actor
// anyway, since these are authored columns.
func (m *mcpTools) actor(ctx context.Context) string {
	name := actorName(mcp.ClientFrom(ctx))
	if name == "" {
		name = m.agent
	}
	return "agent:" + name
}

func flagArgs(f *pflagFlag, value any) []string {
	name := "--" + f.Name
	switch f.Value.Type() {
	case "bool":
		if truthy(value) {
			return []string{name}
		}
		return nil
	case "stringArray", "stringSlice":
		var args []string
		for _, item := range asSlice(value) {
			args = append(args, name, item)
		}
		return args
	default:
		return []string{name, asString(value)}
	}
}

func truthy(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	default:
		return false
	}
}

func asSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return []string{asString(value)}
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, asString(item))
	}
	return out
}

// asString renders a JSON value as the command line wants it. Numbers arrive
// as float64 and must not come out as 1e+06.
func asString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	case bool:
		return fmt.Sprintf("%t", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}
