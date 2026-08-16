package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRepoTrackAndList(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "repo", "track", "--db", db, "scottlaird/roz")
	if err != nil {
		t.Fatalf("repo track returned error: %v", err)
	}
	if got := strings.TrimSpace(out); got != "scottlaird/roz" {
		t.Errorf("repo track printed %q, want the id", got)
	}

	listed, err := runCLI(t, "repo", "list", "--db", db)
	if err != nil {
		t.Fatalf("repo list returned error: %v", err)
	}
	if !strings.Contains(listed, "scottlaird/roz") {
		t.Errorf("repo list does not contain the repository:\n%s", listed)
	}
}

func TestRepoListEmpty(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "repo", "list", "--db", db)
	if err != nil {
		t.Fatalf("repo list returned error: %v", err)
	}
	if !strings.Contains(out, "no tracked repositories") {
		t.Errorf("empty repo list printed %q", out)
	}
}

// TestRepoTrackWithPolicy is the case that prompted the entity: a repository
// nobody reviews for you, stated at the point of tracking it.
func TestRepoTrackWithPolicy(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "repo", "track", "--db", db, "scottlaird/scratch",
		"--pipeline", "direct", "--disposition", "mine alone"); err != nil {
		t.Fatalf("repo track returned error: %v", err)
	}

	object := repoJSON(t, db, "scottlaird/scratch")
	if object["pipeline"] != "direct" {
		t.Errorf("pipeline = %#v, want direct", object["pipeline"])
	}
	if object["disposition"] != "mine alone" {
		t.Errorf("disposition = %#v, want the stated one", object["disposition"])
	}
	// Observed columns are sync's, and untouched by tracking.
	if object["default_branch"] != nil {
		t.Errorf("default_branch = %#v, want null", object["default_branch"])
	}
}

func repoJSON(t *testing.T, db, id string) map[string]any {
	t.Helper()
	out, err := runCLI(t, "repo", "show", "--db", db, id, "-o", "json")
	if err != nil {
		t.Fatalf("repo show returned error: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(out), &object); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	return object
}

func TestRepoSet(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "scottlaird/roz")

	out, err := runCLI(t, "repo", "set", "--db", db, "scottlaird/roz",
		"--pipeline", "direct", "--announce-channel", "#reviews")
	if err != nil {
		t.Fatalf("repo set returned error: %v", err)
	}
	for _, want := range []string{"pipeline", "direct", "announce_channel"} {
		if !strings.Contains(out, want) {
			t.Errorf("set output does not mention %q:\n%s", want, out)
		}
	}

	object := repoJSON(t, db, "scottlaird/roz")
	if object["pipeline"] != "direct" || object["announce_channel"] != "#reviews" {
		t.Errorf("repository = %#v, want the values set", object)
	}
}

func TestRepoSetClearsPolicy(t *testing.T) {
	db := initDB(t)
	if _, err := runCLI(t, "repo", "track", "--db", db, "scottlaird/roz",
		"--pipeline", "direct"); err != nil {
		t.Fatalf("repo track returned error: %v", err)
	}

	if _, err := runCLI(t, "repo", "set", "--db", db, "scottlaird/roz",
		"--pipeline", ""); err != nil {
		t.Fatalf("repo set returned error: %v", err)
	}
	if got := repoJSON(t, db, "scottlaird/roz")["pipeline"]; got != nil {
		t.Errorf("pipeline = %#v, want null after clearing", got)
	}
}

// TestSyncMayNotSetThePipeline is the rule the entity exists to encode.
func TestSyncMayNotSetThePipeline(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "scottlaird/roz")

	_, err := runCLI(t, "repo", "set", "--db", db, "scottlaird/roz",
		"--pipeline", "direct", "--actor", "sync:github")
	if err == nil {
		t.Fatal("sync set the pipeline through the CLI, want an error")
	}
	// The CLI refuses a sync actor before the store even sees it.
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("error = %v, want the sync actor refused", err)
	}
}

func TestRepoRejections(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "no owner", args: []string{"repo", "track", "roz"}, wantErr: "expected owner/name"},
		{name: "empty owner", args: []string{"repo", "track", "/roz"}, wantErr: "no owner"},
		{name: "empty name", args: []string{"repo", "track", "scottlaird/"}, wantErr: "no repository name"},
		{name: "too many parts", args: []string{"repo", "track", "a/b/c"}, wantErr: "more than one"},
		{name: "pull request key", args: []string{"repo", "track", "owner/repo#1"}, wantErr: "pull request key"},
		{
			name:    "unknown pipeline",
			args:    []string{"repo", "track", "scottlaird/roz", "--pipeline", "maybe"},
			wantErr: "is not a pipeline",
		},
		{name: "show untracked", args: []string{"repo", "show", "nobody/nothing"}, wantErr: "no such item"},
		{name: "set untracked", args: []string{"repo", "set", "nobody/nothing", "--disposition", "x"}, wantErr: "no such item"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := initDB(t)
			args := append(tt.args, "--db", db)

			_, err := runCLI(t, args...)
			if err == nil {
				t.Fatalf("%v returned nil, want an error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestRepoSetWithNoFlags(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "scottlaird/roz")

	_, err := runCLI(t, "repo", "set", "--db", db, "scottlaird/roz")
	if err == nil {
		t.Fatal("repo set with no flags returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "nothing to set") {
		t.Errorf("error = %v, want it to say there is nothing to set", err)
	}
}

func TestRepoTrackTwice(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "scottlaird/roz")

	_, err := runCLI(t, "repo", "track", "--db", db, "scottlaird/roz")
	if err == nil {
		t.Fatal("tracking twice returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "already tracked") {
		t.Errorf("error = %v, want it to say the repository is already tracked", err)
	}
}

// TestRepoShortNameIsUnique: the whole value is that a prefix resolves to
// exactly one repository, so a collision is refused when it is written rather
// than settled later by a precedence rule.
func TestRepoShortNameIsUnique(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api-server", "--short-name", "api")

	_, err := runCLI(t, "repo", "track", "--db", db, "acme/web", "--short-name", "api")
	if err == nil {
		t.Fatal("a duplicate short name was accepted")
	}
	// Named as the repository it clashes with, not as a constraint violation.
	if !strings.Contains(err.Error(), "acme/api-server") {
		t.Errorf("error = %v, want it to name the repository holding it", err)
	}
}

func TestRepoShortNameRejections(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api-server")

	for _, bad := range []string{"acme/api", "api#1", "two words"} {
		t.Run(bad, func(t *testing.T) {
			if _, err := runCLI(t, "repo", "set", "acme/api-server", "--db", db,
				"--short-name", bad); err == nil {
				t.Errorf("short name %q was accepted", bad)
			}
		})
	}
}

// TestRepoShortNameClears: an empty value clears it, the way the other
// authored columns work.
func TestRepoShortNameClears(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api-server", "--short-name", "api")

	if _, err := runCLI(t, "repo", "set", "acme/api-server", "--db", db,
		"--short-name", ""); err != nil {
		t.Fatalf("clearing the short name returned error: %v", err)
	}
	// And it is free again.
	trackRepo(t, db, "acme/web", "--short-name", "api")
}

// TestProseExpandsAShortName end to end: the page turns api#1234 into a link
// without the stored text changing.
func TestProseExpandsAShortName(t *testing.T) {
	db := initDB(t)
	trackRepo(t, db, "acme/api-server", "--short-name", "api")
	id := addAction(t, db, "--title", "wait for api#1234", "--verb", "decide")

	page, err := renderIndex(t, db)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(page, "https://github.com/acme/api-server/pull/1234") {
		t.Errorf("the short name did not expand:\n%s", page)
	}
	if !strings.Contains(page, ">api#1234<") {
		t.Errorf("the visible text changed:\n%s", page)
	}
	// The stored value is untouched: this is a rendering rule.
	if got := showActionJSON(t, db, id)["title"]; got != "wait for api#1234" {
		t.Errorf("stored title = %#v, want it unchanged", got)
	}
}
