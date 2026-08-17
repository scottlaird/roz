package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recorder is a Tools that remembers who called it, which is the whole point
// of sessions.
type recorder struct{ calls []string }

func (r *recorder) List() []Tool { return []Tool{{Name: "noop"}} }
func (r *recorder) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	r.calls = append(r.calls, ClientFrom(ctx))
	return "ok", nil
}

func testHTTP(t *testing.T) (*HTTP, *recorder) {
	t.Helper()
	tools := &recorder{}
	return &HTTP{Server: &Server{Name: "roz", Version: "test", Tools: tools}}, tools
}

func post(t *testing.T, h http.Handler, session, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	if session != "" {
		req.Header.Set(SessionHeader, session)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func initialize(t *testing.T, h http.Handler, client string) string {
	t.Helper()
	w := post(t, h, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"`+client+`"}}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("initialize returned %d: %s", w.Code, w.Body)
	}
	id := w.Header().Get(SessionHeader)
	if id == "" {
		t.Fatal("initialize returned no session id")
	}
	return id
}

// TestTwoClientsAreNotEachOther is why sessions exist, and it is not a race
// about a string: every write is recorded as agent:<name>, so the wrong one
// misattributes it in the log — and the actor column exists precisely to be
// trusted about who did what.
//
// The old shape stored the client on the server, so the third call here would
// have been attributed to whoever initialised last.
func TestTwoClientsAreNotEachOther(t *testing.T) {
	h, tools := testHTTP(t)

	alice := initialize(t, h, "alice")
	bob := initialize(t, h, "bob")

	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"noop","arguments":{}}}`
	for _, session := range []string{alice, bob, alice} {
		if w := post(t, h, session, call); w.Code != http.StatusOK {
			t.Fatalf("tools/call returned %d: %s", w.Code, w.Body)
		}
	}

	want := []string{"alice", "bob", "alice"}
	if len(tools.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", tools.calls, want)
	}
	for i := range want {
		if tools.calls[i] != want[i] {
			t.Errorf("call %d was attributed to %q, want %q", i, tools.calls[i], want[i])
		}
	}
}

// TestAnUnknownSessionIsNotFound, which is what buys the clean restart: the
// client sees the 404, re-initializes, and carries on. Anything else has it
// retrying against a server that has forgotten it — or worse, accepted the
// call with no client name at all.
func TestAnUnknownSessionIsNotFound(t *testing.T) {
	h, tools := testHTTP(t)
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"noop","arguments":{}}}`

	for _, name := range []struct{ id, why string }{
		{"", "no session at all"},
		{"deadbeef", "a session this server never issued"},
	} {
		w := post(t, h, name.id, call)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s returned %d, want 404", name.why, w.Code)
		}
	}
	if len(tools.calls) != 0 {
		t.Errorf("a tool ran for an unknown session: %v", tools.calls)
	}
}

// TestGetSaysThereIsNoStream. 405 is how a server declares it offers no
// server-initiated stream, which is spec-legal and all roz needs.
func TestGetSaysThereIsNoStream(t *testing.T) {
	h, _ := testHTTP(t)
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET returned %d, want 405", w.Code)
	}
	if got := w.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow = %q, want POST", got)
	}
}

// TestABrowserCannotBeTurnedIntoAClient is the DNS-rebinding case the spec
// asks about: a page resolving a name to 127.0.0.1 and posting here.
//
// A request with no Origin is allowed, because a header nobody sent is a
// non-browser client — which is every MCP client roz expects.
func TestABrowserCannotBeTurnedIntoAClient(t *testing.T) {
	h, _ := testHTTP(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`

	for _, tt := range []struct {
		origin string
		want   int
	}{
		{"", http.StatusOK},
		{"http://localhost:8737", http.StatusOK},
		{"http://127.0.0.1:8737", http.StatusOK},
		{"http://[::1]:8737", http.StatusOK},
		{"https://evil.example", http.StatusForbidden},
		{"http://roz.evil.example", http.StatusForbidden},
	} {
		name := tt.origin
		if name == "" {
			name = "no origin"
		}
		t.Run(name, func(t *testing.T) {
			w := post(t, h, "", body, "Origin", tt.origin)
			if w.Code != tt.want {
				t.Errorf("origin %q returned %d, want %d", tt.origin, w.Code, tt.want)
			}
		})
	}
}

// TestANotificationIsAccepted rather than answered: no id, no reply.
func TestANotificationIsAccepted(t *testing.T) {
	h, _ := testHTTP(t)
	session := initialize(t, h, "alice")

	w := post(t, h, session, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if w.Code != http.StatusAccepted {
		t.Errorf("a notification returned %d, want 202", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "" {
		t.Errorf("a notification was answered with %q", body)
	}
}

// TestToolsAreTheSameOverBothTransports. The point of one dispatch is that
// stdio and HTTP cannot drift about what roz offers.
func TestToolsAreTheSameOverBothTransports(t *testing.T) {
	h, _ := testHTTP(t)
	session := initialize(t, h, "alice")

	w := post(t, h, session, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var got struct {
		Result struct {
			Tools []Tool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v\n%s", err, w.Body)
	}
	if len(got.Result.Tools) != 1 || got.Result.Tools[0].Name != "noop" {
		t.Errorf("tools = %+v, want the one the Tools reports", got.Result.Tools)
	}
}
