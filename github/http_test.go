package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func serve(t *testing.T, handler http.HandlerFunc) Runner {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return HTTPRunner(srv.Client(), srv.URL, StaticToken("sekrit"))
}

func TestHTTPRunnerSendsTheQuery(t *testing.T) {
	run := serve(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "bearer sekrit" ||
			!strings.Contains(string(body), `"query":"query { viewer { login } }"`) {
			t.Errorf("request = %s %v %s", r.Method, r.Header, body)
		}
		_, _ = io.WriteString(w, `{"data":{"viewer":{"login":"octocat"}}}`)
	})
	body, err := run(context.Background(), "query { viewer { login } }")
	if err != nil || !strings.Contains(string(body), "octocat") {
		t.Fatalf("run() = %s, %v", body, err)
	}
}

// A partial failure is a 200 with an errors array, and the rest of it is
// good: it goes to the parser, as gh's output does.
func TestHTTPRunnerReturnsPartialFailures(t *testing.T) {
	const partial = `{"data":{"a0":null,"a1":{"number":2}},"errors":[{"message":"Could not resolve to a PullRequest"}]}`
	run := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, partial) })
	body, err := run(context.Background(), "q")
	if err != nil || string(body) != partial {
		t.Fatalf("run() = %s, %v", body, err)
	}
}

func TestHTTPRunnerFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		limited bool
		want    string
	}{
		{name: "too many requests", status: 429, limited: true},
		{name: "secondary limit", status: 403, headers: map[string]string{"Retry-After": "60"},
			body: `{"message":"You have exceeded a secondary rate limit"}`, limited: true, want: "retry after 60s"},
		{name: "budget spent", status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0"},
			body: `{"message":"API rate limit exceeded"}`, limited: true},
		{name: "limit announced in a page", status: 200,
			body: "<html><body>You have exceeded a secondary rate limit</body></html>", limited: true},
		{name: "bad credentials", status: 401, body: `{"message":"Bad credentials"}`, want: "Bad credentials"},
		{name: "forbidden", status: 403, body: `{"message":"Resource not accessible by integration"}`, want: "not accessible"},
		{name: "bad gateway page", status: 502, body: "<html>502</html>", want: "HTTP 502"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := serve(t, func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			body, err := run(context.Background(), "q")
			if err == nil {
				t.Fatalf("run() = %s, want an error", body)
			}
			if errors.Is(err, ErrRateLimited) != tc.limited {
				t.Errorf("error = %v, rate limited = %v, want %v", err, !tc.limited, tc.limited)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestHTTPRunnerAsksForATokenEachTime(t *testing.T) {
	n := 0
	tokens := func(context.Context) (string, error) {
		n++
		if n > 1 {
			return "", errors.New("expired, and the renewal failed")
		}
		return "first", nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	t.Cleanup(srv.Close)
	run := HTTPRunner(srv.Client(), srv.URL, tokens)
	if _, err := run(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(context.Background(), "q"); err == nil || !strings.Contains(err.Error(), "renewal failed") {
		t.Errorf("second run: %v, want the token source's error", err)
	}
}

func TestHTTPRunnerHoldsAfterALimit(t *testing.T) {
	calls := 0
	run := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"You have exceeded a secondary rate limit"}`)
	})
	for range 3 {
		if _, err := run(context.Background(), "q"); !errors.Is(err, ErrRateLimited) {
			t.Fatalf("error = %v, want ErrRateLimited", err)
		}
	}
	if calls != 1 {
		t.Errorf("GitHub was asked %d times during a minute's hold, want once", calls)
	}
}

func TestLimitEnds(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	resp := func(h map[string]string) *http.Response {
		r := &http.Response{Header: http.Header{}}
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    time.Time
	}{
		{"retry after", map[string]string{"Retry-After": "30"}, now.Add(30 * time.Second)},
		{"budget reset", map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1000600"}, time.Unix(1_000_600, 0)},
		{"nothing said", nil, now.Add(time.Minute)},
	} {
		if got := limitEnds(resp(tc.headers), now); !got.Equal(tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
