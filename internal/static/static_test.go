package static

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fetch(t *testing.T, path string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, path, nil)
	for name, value := range header {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, request)
	return recorder
}

// TestAKnownCopyIsNotSentAgain is the whole feature.
func TestAKnownCopyIsNotSentAgain(t *testing.T) {
	first := fetch(t, URL("roz.css"), nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first GET = %d, want 200", first.Code)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag, so nothing can be revalidated")
	}
	if first.Body.Len() == 0 {
		t.Fatal("the stylesheet is empty")
	}

	second := fetch(t, URL("roz.css"), map[string]string{"If-None-Match": etag})
	if second.Code != http.StatusNotModified {
		t.Errorf("second GET = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("a 304 carried %d bytes of body", second.Body.Len())
	}
}

// TestTheTagSurvivesARestart is the part of the issue that decides the design.
//
// A modification time would not: an embedded file's is whatever the build
// says, so two processes — or two builds of identical bytes — would disagree
// about whether the browser's copy is current, and every restart would cost a
// full download of every asset. The tag is the content's hash, so it is the
// same in the next process for the same reason it is the same in this one.
func TestTheTagSurvivesARestart(t *testing.T) {
	before, ok := ETag("roz.css")
	if !ok {
		t.Fatal("no such file")
	}

	// A second load of the embedded files stands in for the next process.
	assets = load()

	after, _ := ETag("roz.css")
	if after != before {
		t.Errorf("the tag changed across a reload: %s then %s", before, after)
	}
}

// TestTheTagIsTheContent: two files with the same bytes get the same tag and
// two with different bytes do not, which is what makes it safe to serve one
// stable URL forever.
func TestTheTagIsTheContent(t *testing.T) {
	same := etagOf([]byte("body{color:red}"))
	again := etagOf([]byte("body{color:red}"))
	different := etagOf([]byte("body{color:blue}"))

	if same != again {
		t.Errorf("the same bytes hashed to %s and %s", same, again)
	}
	if same == different {
		t.Error("different bytes hashed the same")
	}
	if !strings.HasPrefix(same, `"`) || !strings.HasSuffix(same, `"`) {
		t.Errorf("ETag %s is not quoted; the header grammar requires it", same)
	}
}

// TestAWeakTagStillMatches: a proxy may weaken a tag, and for "may I reuse
// what I have" a weak match is still a match.
func TestAWeakTagStillMatches(t *testing.T) {
	etag, _ := ETag("roz.css")

	for _, header := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
		got := fetch(t, URL("roz.css"), map[string]string{"If-None-Match": header})
		if got.Code != http.StatusNotModified {
			t.Errorf("If-None-Match %s = %d, want 304", header, got.Code)
		}
	}

	stale := fetch(t, URL("roz.css"), map[string]string{"If-None-Match": `"stale"`})
	if stale.Code != http.StatusOK {
		t.Errorf("a stale tag = %d, want 200 and the new content", stale.Code)
	}
}

// TestNothingElseIsServed: the handler answers for the files that are compiled
// in and nothing else — a path that walks out of the set is a 404 rather than
// a read of whatever it names.
func TestNothingElseIsServed(t *testing.T) {
	for _, path := range []string{
		URL("nope.css"),
		URL("../render.go"),
		URL("sub/roz.css"),
		Prefix,
	} {
		if got := fetch(t, path, nil); got.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, got.Code)
		}
	}
}

// TestRevalidationIsAskedFor: without it a browser may hold a stale copy for a
// heuristic interval, and a stylesheet lagging the page it styles is worse
// than a round trip that ends in 304.
func TestRevalidationIsAskedFor(t *testing.T) {
	got := fetch(t, URL("roz.css"), nil)
	if cache := got.Header().Get("Cache-Control"); cache != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cache)
	}
	if kind := got.Header().Get("Content-Type"); !strings.HasPrefix(kind, "text/css") {
		t.Errorf("Content-Type = %q, want text/css", kind)
	}
}

// TestQueueBandsOutrankTheBaseRule is a regression guard for the bug behind
// #92, which was not the one the issue described.
//
// The queue's rank-class colours are written as `li.decide` — one class and
// one element — while the rule they have to beat is `ol.queue li`, one class
// and two. The base rule won every time, so no rank colour ever reached the
// page and the whole list drew as --raise with a --rule edge whatever its
// verb. It reads exactly like colours that are too similar, which is how it
// survived being looked at.
//
// A stylesheet is not parsed here, so this is a text rule rather than a
// specificity calculation: every selector naming a band must carry the ol.queue
// prefix that outranks the base.
func TestQueueBandsOutrankTheBaseRule(t *testing.T) {
	css, err := Read("roz.css")
	if err != nil {
		t.Fatalf("Read() returned error: %v", err)
	}

	bands := []string{"click", "decide", "session", "wait", "late", "expired"}
	for _, line := range strings.Split(css, "\n") {
		selector, _, isRule := strings.Cut(line, "{")
		if !isRule || strings.HasPrefix(strings.TrimSpace(line), "/*") {
			continue
		}
		for _, part := range strings.Split(selector, ",") {
			part = strings.TrimSpace(part)
			for _, band := range bands {
				if !strings.HasPrefix(part, "li."+band) {
					continue
				}
				t.Errorf("%q styles a queue band without outranking `ol.queue li`, "+
					"so it will not apply; qualify it as `ol.queue %s`", part, part)
			}
		}
	}
}

// TestEveryQueueBandIsColoured: a band with no tint is the other half of #92 —
// session had none, although --session-bg had been in the palette from the
// start, so `write` items were indistinguishable from the page.
func TestEveryQueueBandIsColoured(t *testing.T) {
	css, err := Read("roz.css")
	if err != nil {
		t.Fatalf("Read() returned error: %v", err)
	}
	for _, band := range []string{"click", "decide", "session", "wait"} {
		rule := "ol.queue li." + band + "{"
		at := strings.Index(css, rule)
		if at < 0 {
			t.Errorf("no rule for the %s band", band)
			continue
		}
		body := css[at+len(rule):]
		body = body[:strings.Index(body, "}")]
		for _, want := range []string{"background:", "border-left-color:"} {
			if !strings.Contains(body, want) {
				t.Errorf("the %s band sets no %s, so it reads as an uncoloured row", band, want)
			}
		}
	}
}
