package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// SessionHeader is where a client carries its session id, as the spec names
// it.
const SessionHeader = "Mcp-Session-Id"

// sessionIdle is how long a session survives without a request.
//
// Long enough that a client thinking between tool calls is never surprised,
// short enough that a process left running for a week is not holding a map of
// every agent that ever connected to it.
const sessionIdle = 2 * time.Hour

// HTTP serves the protocol over HTTP, so the server can be restarted without
// restarting the session that uses it.
//
// A stdio server cannot be: the client spawns the process and never reconnects
// it, so a schema change costs a whole session — `roz mcp` exits when the
// database migrates under it, which is correct, and nothing brings it back.
// Over HTTP the client reconnects on its own, and the same event costs a
// `roz serve` restart instead.
//
// POST only. Returning 405 to GET is how a server declares it offers no
// server-initiated stream, which is spec-legal and is all roz needs: every
// exchange here is a request and its answer.
type HTTP struct {
	Server *Server

	// One tool call at a time. A call runs the CLI's own command tree, and
	// while #73 took the database off a package variable, cobra still holds
	// flag state per command — so two in flight would be sharing it. Stdio
	// gets this for free by reading one line at a time; here it has to be
	// said.
	calls sync.Mutex

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	*Session
	lastSeen time.Time
}

func (h *HTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// Not an error the client should retry differently: it is a statement
		// about what this endpoint is.
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "this endpoint answers POST; it offers no server-initiated stream",
			http.StatusMethodNotAllowed)
		return
	}
	// Required by the spec, and it stops one specific attack: a page in a
	// browser resolving a name to 127.0.0.1 and posting to this. It does
	// nothing about another process on the same machine, which is why the
	// endpoint is off by default.
	if !originAllowed(r.Header.Get("Origin")) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}

	var req request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLine)).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, response{
			JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{Code: codeParse, Message: "not valid JSON"},
		})
		return
	}

	sess, id, ok := h.sessionFor(r.Header.Get(SessionHeader), req.Method)
	if !ok {
		// An unknown session is a 404, which is what buys the clean restart:
		// the client sees it, re-initializes, and carries on. Anything else
		// would have it retrying against a server that has forgotten it.
		http.Error(w, "no such session; initialize again", http.StatusNotFound)
		return
	}
	if id != "" {
		w.Header().Set(SessionHeader, id)
	}

	h.calls.Lock()
	result, rpcErr := h.Server.dispatch(r.Context(), sess, req)
	h.calls.Unlock()

	if req.isNotification() {
		// No id, no reply. 202 rather than an empty 200, since there is
		// nothing to read rather than nothing to say.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, response{
		JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr,
	})
}

// sessionFor finds or opens the session a request belongs to.
//
// initialize opens one and is the only method that may arrive without an id.
// Everything else must name a session this server still knows, so that a
// restarted server does not silently accept calls from a client that thinks it
// initialized — and, more to the point, does not attribute them to an empty
// client name.
func (h *HTTP) sessionFor(id, method string) (*Session, string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expire()

	if method == "initialize" {
		fresh := &session{Session: &Session{}, lastSeen: time.Now()}
		id := newSessionID()
		if h.sessions == nil {
			h.sessions = map[string]*session{}
		}
		h.sessions[id] = fresh
		return fresh.Session, id, true
	}
	if id == "" {
		return nil, "", false
	}
	found, ok := h.sessions[id]
	if !ok {
		return nil, "", false
	}
	found.lastSeen = time.Now()
	return found.Session, id, true
}

// expire drops sessions nothing has used. Called on the way in rather than on
// a timer: there is no work to do when nothing is arriving.
func (h *HTTP) expire() {
	for id, s := range h.sessions {
		if time.Since(s.lastSeen) > sessionIdle {
			delete(h.sessions, id)
		}
	}
}

func newSessionID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// Only reachable if the system entropy source fails, at which point
		// a predictable id is the smaller problem. Time is enough to keep two
		// sessions apart in that case.
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(raw[:])
}

// originAllowed answers the DNS-rebinding question the spec asks about.
//
// No Origin at all is allowed: a header nobody sent is a non-browser client,
// which is every MCP client roz expects. What is refused is a browser page
// claiming an origin that is not this machine.
func originAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Sessions reports how many are open, for a caller that wants to say so.
func (h *HTTP) Sessions() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sessions)
}
