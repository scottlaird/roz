// Package mcp serves the tool over the Model Context Protocol, so an agent
// can read the queue and write to it without shelling out.
//
// Transport is JSON-RPC 2.0 over stdio, hand-rolled against the standard
// library. The surface an agent needs is small — a handshake, a list, a call
// — and writing it here keeps the dependency list where it is.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Protocol versions this speaks. A client asking for one of these gets it
// back; anything else gets the newest, and may decide for itself whether to
// carry on.
var supportedVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC error codes, from the specification.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// maxLine is the longest single message accepted. Arguments are short; a
// megabyte is far past anything legitimate and stops a malformed stream from
// growing the buffer without limit.
const maxLine = 1 << 20

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// isNotification reports whether no reply is wanted. A notification has no
// id, and answering one is a protocol error rather than a courtesy.
func (r request) isNotification() bool { return len(r.ID) == 0 }

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Tools is what the server exposes. The implementation lives next door, so
// this file knows about the protocol and nothing about todo.
type Tools interface {
	// List returns the callable tools.
	List() []Tool
	// Call runs one and returns what it printed. A tool that refuses input
	// returns an error, which is reported to the caller as a failed tool
	// call rather than as a broken connection.
	Call(ctx context.Context, name string, arguments map[string]any) (string, error)
}

// Tool is one callable, described for the client.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Server speaks the protocol over one pair of streams.
type Server struct {
	// Name and Version identify this server to the client.
	Name    string
	Version string
	// client is what the other end called itself at initialize, which is as
	// close to an identity as the protocol offers.
	client string
	// Tools is what it exposes.
	Tools Tools
	// Log receives anything worth saying. It must not be stdout, which
	// carries the protocol.
	Log io.Writer
}

// Serve reads requests until the stream ends or ctx is cancelled.
//
// Requests are handled one at a time, in order. That is not only simpler: a
// command is executed by running the CLI's own command tree, which keeps some
// state in package-level flags, so two at once would race.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLine)
	encoder := json.NewEncoder(out)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil // cancellation is how this is meant to end
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			s.write(encoder, response{
				JSONRPC: "2.0", ID: json.RawMessage("null"),
				Error: &rpcError{Code: codeParse, Message: "not valid JSON"},
			})
			continue
		}

		result, rpcErr := s.dispatch(ctx, req)
		if req.isNotification() {
			continue // no id, no reply, whatever happened
		}
		s.write(encoder, response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr})
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("reading the request stream: %w", err)
	}
	return nil
}

func (s *Server) write(encoder *json.Encoder, r response) {
	if err := encoder.Encode(r); err != nil {
		s.logf("writing a response: %v\n", err)
	}
}

func (s *Server) dispatch(ctx context.Context, req request) (any, *rpcError) {
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return nil, &rpcError{Code: codeInvalidRequest, Message: "expected jsonrpc 2.0"}
	}

	switch req.Method {
	case "initialize":
		return s.initialize(req.Params), nil
	case "notifications/initialized", "notifications/cancelled":
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.Tools.List()}, nil
	case "tools/call":
		return s.callTool(ctx, req.Params)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "no method " + req.Method}
	}
}

func (s *Server) initialize(params json.RawMessage) any {
	var asked struct {
		ProtocolVersion string `json:"protocolVersion"`
		ClientInfo      struct {
			Name string `json:"name"`
		} `json:"clientInfo"`
	}
	_ = json.Unmarshal(params, &asked)
	s.client = asked.ClientInfo.Name

	return map[string]any{
		"protocolVersion": negotiate(asked.ProtocolVersion),
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
	}
}

// negotiate answers with the version asked for when it is one this speaks,
// and with the newest it knows otherwise. The surface here is small enough
// that the revisions do not differ over it.
func negotiate(asked string) string {
	for _, known := range supportedVersions {
		if asked == known {
			return asked
		}
	}
	return supportedVersions[0]
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "arguments are not an object"}
	}
	if call.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "no tool named"}
	}

	output, err := s.Tools.Call(WithClient(ctx, s.client), call.Name, call.Arguments)
	if err != nil {
		// A tool that refused its input is a result, not a transport
		// failure: the agent should see the message and try again, rather
		// than lose the connection.
		return toolResult(err.Error(), true), nil
	}
	return toolResult(output, false), nil
}

func toolResult(text string, failed bool) any {
	if text == "" {
		text = "(no output)"
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": failed,
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.Log == nil {
		return
	}
	fmt.Fprintf(s.Log, format, args...)
}

// clientKey carries the client's name to the tools, which turn it into the
// actor a change is recorded under.
type clientKey struct{}

// WithClient records who is calling.
func WithClient(ctx context.Context, name string) context.Context {
	if name == "" {
		return ctx
	}
	return context.WithValue(ctx, clientKey{}, name)
}

// ClientFrom returns what the caller called itself, or "" if it said nothing.
func ClientFrom(ctx context.Context) string {
	name, _ := ctx.Value(clientKey{}).(string)
	return name
}
