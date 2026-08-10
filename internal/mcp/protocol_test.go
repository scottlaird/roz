package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// fakeTools stands in for the command tree.
type fakeTools struct {
	called    string
	arguments map[string]any
	output    string
	err       error
	client    string
}

func (f *fakeTools) List() []Tool {
	return []Tool{{
		Name:        "thing_do",
		Description: "does the thing",
		InputSchema: map[string]any{"type": "object"},
	}}
}

func (f *fakeTools) Call(ctx context.Context, name string, arguments map[string]any) (string, error) {
	f.called, f.arguments, f.client = name, arguments, ClientFrom(ctx)
	return f.output, f.err
}

// exchange runs the requests through a server and returns the replies.
func exchange(t *testing.T, tools Tools, requests ...string) []map[string]any {
	t.Helper()

	in := strings.NewReader(strings.Join(requests, "\n") + "\n")
	var out strings.Builder
	s := &Server{Name: "todo", Version: "test", Tools: tools}
	if err := s.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("Serve() returned error: %v", err)
	}

	var replies []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var reply map[string]any
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatalf("reply is not JSON: %v\n%s", err, line)
		}
		replies = append(replies, reply)
	}
	return replies
}

func TestInitialize(t *testing.T) {
	replies := exchange(t, &fakeTools{},
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)

	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1", len(replies))
	}
	result := replies[0]["result"].(map[string]any)
	if got := result["protocolVersion"]; got != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want the one asked for", got)
	}
	if _, ok := result["capabilities"].(map[string]any)["tools"]; !ok {
		t.Errorf("capabilities = %v, want tools among them", result["capabilities"])
	}
}

// TestUnknownVersionGetsOurNewest: a client asking for something we do not
// know should get an answer it can decide about, not silence.
func TestUnknownVersionGetsOurNewest(t *testing.T) {
	replies := exchange(t, &fakeTools{},
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)

	result := replies[0]["result"].(map[string]any)
	if got := result["protocolVersion"]; got != supportedVersions[0] {
		t.Errorf("protocolVersion = %v, want %v", got, supportedVersions[0])
	}
}

// TestNotificationsGetNoReply: answering one is a protocol error, not a
// courtesy.
func TestNotificationsGetNoReply(t *testing.T) {
	replies := exchange(t, &fakeTools{},
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`)

	if len(replies) != 1 {
		t.Fatalf("got %d replies, want only the one for ping", len(replies))
	}
	if replies[0]["id"] != float64(1) {
		t.Errorf("the reply is for id %v, want 1", replies[0]["id"])
	}
}

func TestToolsList(t *testing.T) {
	replies := exchange(t, &fakeTools{}, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	tools := replies[0]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	if got := tools[0].(map[string]any)["name"]; got != "thing_do" {
		t.Errorf("name = %v", got)
	}
}

func TestToolsCall(t *testing.T) {
	tools := &fakeTools{output: "it is done"}
	replies := exchange(t, tools,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"someone"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"thing_do","arguments":{"how":"carefully"}}}`)

	if tools.called != "thing_do" || tools.arguments["how"] != "carefully" {
		t.Errorf("called %q with %v", tools.called, tools.arguments)
	}
	// The client's name reaches the tool, which is how a change gets
	// attributed to whoever asked for it.
	if tools.client != "someone" {
		t.Errorf("client = %q, want it carried through", tools.client)
	}

	result := replies[1]["result"].(map[string]any)
	if result["isError"] != false {
		t.Errorf("isError = %v, want false", result["isError"])
	}
	content := result["content"].([]any)[0].(map[string]any)
	if content["text"] != "it is done" {
		t.Errorf("text = %v", content["text"])
	}
}

// TestARefusedToolIsAResult: an agent should see what it did wrong and try
// again, rather than lose the connection over it.
func TestARefusedToolIsAResult(t *testing.T) {
	tools := &fakeTools{err: errUnusable{}}
	replies := exchange(t, tools,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"thing_do","arguments":{}}}`)

	result := replies[0]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("isError = %v, want true", result["isError"])
	}
	if replies[0]["error"] != nil {
		t.Errorf("a refused tool became a transport error: %v", replies[0]["error"])
	}
	content := result["content"].([]any)[0].(map[string]any)
	if !strings.Contains(content["text"].(string), "not a verb") {
		t.Errorf("text = %v, want the reason", content["text"])
	}
}

type errUnusable struct{}

func (errUnusable) Error() string { return `"teleport" is not a verb` }

func TestProtocolErrors(t *testing.T) {
	tests := []struct {
		name    string
		request string
		code    float64
	}{
		{"not JSON", `{oh dear`, codeParse},
		{"unknown method", `{"jsonrpc":"2.0","id":1,"method":"sing"}`, codeMethodNotFound},
		{"wrong version", `{"jsonrpc":"1.0","id":1,"method":"ping"}`, codeInvalidRequest},
		{"call with no name", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`, codeInvalidParams},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			replies := exchange(t, &fakeTools{}, tt.request)
			if len(replies) != 1 {
				t.Fatalf("got %d replies, want 1", len(replies))
			}
			failure, ok := replies[0]["error"].(map[string]any)
			if !ok {
				t.Fatalf("reply has no error: %v", replies[0])
			}
			if failure["code"] != tt.code {
				t.Errorf("code = %v, want %v", failure["code"], tt.code)
			}
		})
	}
}

// TestEmptyOutputIsStillContent: a tool that printed nothing succeeded, and
// an empty content array reads like a failure.
func TestEmptyOutputIsStillContent(t *testing.T) {
	replies := exchange(t, &fakeTools{output: ""},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"thing_do"}}`)

	content := replies[0]["result"].(map[string]any)["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v, want one entry", content)
	}
}
