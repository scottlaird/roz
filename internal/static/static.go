// Package static is the files roz serves that are not the page: the
// stylesheet today, and whatever else earns its place later.
//
// Compiled in, so there is nothing to install and nothing to find at runtime.
// A tool that renders a page from a local database should not also need a
// directory of assets to be where it expects.
package static

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed roz.css
var files embed.FS

// Prefix is where these are served from.
const Prefix = "/static/"

// asset is one file, with the answer to "has this changed" worked out once.
type asset struct {
	body []byte
	etag string
	kind string
}

// assets is every embedded file, hashed at startup.
//
// At startup rather than per request: there are a handful of them and they
// cannot change while the process runs, so hashing on each request would be
// work done to reach a conclusion already known.
var assets = load()

func load() map[string]asset {
	loaded := map[string]asset{}
	err := fs.WalkDir(files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		loaded[name] = asset{body: body, etag: etagOf(body), kind: kindOf(name)}
		return nil
	})
	if err != nil {
		// The files are embedded, so this is a build that cannot read itself.
		panic(fmt.Sprintf("reading embedded static files: %v", err))
	}
	return loaded
}

// etagOf is the content's own name for itself.
//
// A hash rather than a modification time, which is what makes a 304 survive a
// restart — and a redeploy, and a rebuild that changed nothing. An embedded
// file has no useful mtime anyway: it is whatever the build says, so two
// builds of identical bytes would disagree about whether the browser's copy is
// current.
//
// Strong rather than weak: the bytes are the bytes. Quoted because the header
// grammar requires it, and an unquoted ETag is quietly ignored by some caches.
func etagOf(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

func kindOf(name string) string {
	switch path.Ext(name) {
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".png":
		return "image/png"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

// URL is where a file is served from, with no cache-busting in the path.
//
// Deliberately stable: a content-addressed URL would never need revalidating,
// but it would also never produce the 304 this is for, and it would change the
// page's markup every time a colour did. One URL, one conditional request, one
// 304 — which for a loopback tool is the right shape.
func URL(name string) string { return Prefix + name }

// ETag is a file's tag, for a caller that wants to know without a request.
func ETag(name string) (string, bool) {
	a, ok := assets[name]
	return a.etag, ok
}

// Read returns a file's contents, for the caller that inlines rather than
// links it — `roz render` writes a page that is opened from disk, where a
// second request has nowhere to go.
func Read(name string) (string, error) {
	a, ok := assets[name]
	if !ok {
		return "", fmt.Errorf("no static file %q", name)
	}
	return string(a.body), nil
}

// Handler serves the embedded files, answering 304 where the browser already
// has them.
//
// http.ServeContent would do the conditional handling, and is not used: it
// wants an io.ReadSeeker and a modification time, and the modification time is
// the thing being avoided. The If-None-Match comparison is three lines and
// says what it means.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(r.URL.Path, Prefix)
		a, ok := assets[name]
		if !ok || strings.Contains(name, "/") {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", a.kind)
		w.Header().Set("ETag", a.etag)
		// no-cache asks for revalidation rather than forbidding storage, which
		// is what produces a conditional request and therefore a 304. Without
		// it a browser may hold a stale copy for a heuristic interval, and a
		// stylesheet that lags the page it styles is worse than a round trip.
		w.Header().Set("Cache-Control", "no-cache")

		if matches(r.Header.Get("If-None-Match"), a.etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		http.ServeContent(w, r, name, time.Time{}, strings.NewReader(string(a.body)))
	})
}

// matches reports whether the browser already has this exact content.
//
// If-None-Match carries a list, and a proxy may have weakened a tag by
// prefixing W/ — which for a comparison about "may I reuse this" is still a
// match, since a weak tag means the representations are equivalent.
func matches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}
