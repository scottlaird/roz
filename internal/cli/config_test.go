package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigShowOnANewDatabase(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "config", "show", "--db", db)
	if err != nil {
		t.Fatalf("config show returned error: %v", err)
	}
	for _, want := range []string{"owner", "jira_base_url", "jira_prefixes"} {
		if !strings.Contains(out, want) {
			t.Errorf("config show does not mention %q:\n%s", want, out)
		}
	}
}

func TestConfigSetThenShow(t *testing.T) {
	db := initDB(t)

	out, err := runCLI(t, "config", "set", "--db", db,
		"--owner", "scott",
		"--jira-base-url", "https://example.atlassian.net/browse",
		"--jira-prefix", "cdss", "--jira-prefix", "PLAT")
	if err != nil {
		t.Fatalf("config set returned error: %v", err)
	}
	if !strings.Contains(out, "owner") || !strings.Contains(out, "scott") {
		t.Errorf("config set did not report the change:\n%s", out)
	}

	shown, err := runCLI(t, "config", "show", "--db", db, "-o", "json")
	if err != nil {
		t.Fatalf("config show returned error: %v", err)
	}
	var cfg struct {
		Owner        string   `json:"owner"`
		JiraBaseURL  string   `json:"jira_base_url"`
		JiraPrefixes []string `json:"jira_prefixes"`
	}
	if err := json.Unmarshal([]byte(shown), &cfg); err != nil {
		t.Fatalf("config show -o json returned %q: %v", shown, err)
	}
	if cfg.Owner != "scott" {
		t.Errorf("owner = %q, want %q", cfg.Owner, "scott")
	}
	if cfg.JiraBaseURL != "https://example.atlassian.net/browse" {
		t.Errorf("jira_base_url = %q", cfg.JiraBaseURL)
	}
	if len(cfg.JiraPrefixes) != 2 || cfg.JiraPrefixes[0] != "CDSS" || cfg.JiraPrefixes[1] != "PLAT" {
		t.Errorf("jira_prefixes = %v, want [CDSS PLAT] — upper cased, order kept", cfg.JiraPrefixes)
	}
}

// TestConfigSetTrimsATrailingSlash: the stored value is the one the log and
// `config show` report, so the tidying happens once, on the way in.
func TestConfigSetTrimsATrailingSlash(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "config", "set", "--db", db,
		"--jira-base-url", "https://example.atlassian.net/browse/"); err != nil {
		t.Fatalf("config set returned error: %v", err)
	}
	out, err := runCLI(t, "config", "show", "--db", db)
	if err != nil {
		t.Fatalf("config show returned error: %v", err)
	}
	if !strings.Contains(out, "https://example.atlassian.net/browse\n") {
		t.Errorf("the trailing slash was kept:\n%s", out)
	}
}

// TestConfigSetReplacesThePrefixList: adding to it would leave no way to
// remove one, so passing none clears it.
func TestConfigSetReplacesThePrefixList(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "config", "set", "--db", db, "--jira-prefix", "CDSS"); err != nil {
		t.Fatalf("config set returned error: %v", err)
	}
	if _, err := runCLI(t, "config", "set", "--db", db, "--jira-prefix", "PLAT"); err != nil {
		t.Fatalf("config set returned error: %v", err)
	}
	out, err := runCLI(t, "config", "show", "--db", db, "-o", "json")
	if err != nil {
		t.Fatalf("config show returned error: %v", err)
	}
	if strings.Contains(out, "CDSS") {
		t.Errorf("the second --jira-prefix added rather than replaced:\n%s", out)
	}

	if _, err := runCLI(t, "config", "set", "--db", db, "--jira-prefix", ""); err != nil {
		t.Fatalf("clearing the prefixes returned error: %v", err)
	}
	cleared, err := runCLI(t, "config", "show", "--db", db, "-o", "json")
	if err != nil {
		t.Fatalf("config show returned error: %v", err)
	}
	if strings.Contains(cleared, "PLAT") {
		t.Errorf("--jira-prefix \"\" did not clear the list:\n%s", cleared)
	}
}

// TestConfigSetIsIdempotent: setting a value it already has writes nothing,
// which is the diff doing its job rather than the command checking.
func TestConfigSetIsIdempotent(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "config", "set", "--db", db, "--owner", "scott"); err != nil {
		t.Fatalf("config set returned error: %v", err)
	}
	out, err := runCLI(t, "config", "set", "--db", db, "--owner", "scott")
	if err != nil {
		t.Fatalf("config set returned error: %v", err)
	}
	if !strings.Contains(out, "unchanged") {
		t.Errorf("config set reported %q, want it unchanged", out)
	}
}

func TestConfigSetRejections(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "nothing to set",
			args:    []string{"config", "set"},
			wantErr: "nothing to set",
		},
		{
			name:    "a base URL with no scheme",
			args:    []string{"config", "set", "--jira-base-url", "example.atlassian.net/browse"},
			wantErr: "scheme",
		},
		{
			name:    "a prefix the linker could never match",
			args:    []string{"config", "set", "--jira-prefix", "C"},
			wantErr: "project key",
		},
		{
			name:    "sync may not write settings",
			args:    []string{"config", "set", "--owner", "scott", "--actor", "sync:github"},
			wantErr: "sync actors are set by",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := initDB(t)
			_, err := runCLI(t, append(tt.args, "--db", db)...)
			if err == nil {
				t.Fatalf("%v was accepted, want an error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// TestConfigSetIsLogged: a settings change is exactly the sort of thing worth
// finding in the log later, and getting that for free is why these are
// columns rather than a key-value bag.
func TestConfigSetIsLogged(t *testing.T) {
	db := initDB(t)

	if _, err := runCLI(t, "config", "set", "--db", db,
		"--jira-base-url", "https://example.atlassian.net/browse"); err != nil {
		t.Fatalf("config set returned error: %v", err)
	}

	out, err := runCLI(t, "watch", "--db", db, "--once")
	if err != nil {
		t.Fatalf("watch returned error: %v", err)
	}
	if !strings.Contains(out, "jira_base_url") || !strings.Contains(out, "config") {
		t.Errorf("the log does not show the settings change:\n%s", out)
	}
}
