package store

import (
	"context"
	"strings"
	"testing"
)

// TestConfigIsSeeded: the migration writes the row, so no reader ever has to
// decide what an absent configuration means.
func TestConfigIsSeeded(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	cfg, err := st.Config(ctx)
	if err != nil {
		t.Fatalf("Config() returned error: %v", err)
	}

	if got, want := cfg.ID, ConfigID; got != want {
		t.Errorf("id = %q, want %q", got, want)
	}
	if cfg.Owner != "" || cfg.JiraBaseURL != "" {
		t.Errorf("a new database is configured: owner = %q, jira base = %q", cfg.Owner, cfg.JiraBaseURL)
	}
	prefixes, err := cfg.Prefixes()
	if err != nil {
		t.Fatalf("Prefixes() returned error: %v", err)
	}
	if len(prefixes) != 0 {
		t.Errorf("prefixes = %v, want none: an unconfigured queue links no keys", prefixes)
	}
	if cfg.CreatedAt == "" {
		t.Error("created_at is empty")
	}
}

// TestConfigCannotHaveASecondRow: one row is a schema rule, not a convention
// the commands are trusted to keep.
func TestConfigCannotHaveASecondRow(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	_, err := st.db.ExecContext(ctx,
		"INSERT INTO config (id, created_at, updated_at) VALUES ('other', '', '')")
	if err == nil {
		t.Fatal("a second config row was accepted, want the CHECK to refuse it")
	}
}

// TestSaveConfigLogsEachSetting: settings get the same diff-based log as
// everything else, which is the argument for columns over a key-value bag.
func TestSaveConfigLogsEachSetting(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	before, err := st.Config(ctx)
	if err != nil {
		t.Fatalf("Config() returned error: %v", err)
	}
	after := before.Clone()
	after.Owner = "scott"
	after.JiraBaseURL = "https://example.atlassian.net/browse"

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	changes, err := tx.SaveConfig(ctx, before, after)
	if err != nil {
		t.Fatalf("SaveConfig() returned error: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("SaveConfig() reported %d changes, want 2: %v", len(changes), changes)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	events, err := st.Events(ctx, EventQuery{Kind: "changed"})
	if err != nil {
		t.Fatalf("Events() returned error: %v", err)
	}
	logged := make(map[string]string, len(events))
	for _, e := range events {
		if e.SubjectType != "config" {
			continue
		}
		if e.SubjectID != ConfigID {
			t.Errorf("event subject_id = %q, want %q", e.SubjectID, ConfigID)
		}
		logged[e.Field] = e.NewValue
	}
	if got, want := logged["owner"], "scott"; got != want {
		t.Errorf("logged owner = %q, want %q", got, want)
	}
	if got, want := logged["jira_base_url"], "https://example.atlassian.net/browse"; got != want {
		t.Errorf("logged jira_base_url = %q, want %q", got, want)
	}
}

// TestSaveConfigRejectsAnUnusableJiraBase: a scheme-less base resolves
// against whatever host the page is served from, which is the failure that is
// hardest to spot — the link is there and goes somewhere.
func TestSaveConfigRejectsAnUnusableJiraBase(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	tests := []struct {
		name, base string
	}{
		{"relative", "example.atlassian.net/browse"},
		{"a path", "/browse"},
		{"the wrong scheme", "ftp://example.atlassian.net/browse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, err := st.Config(ctx)
			if err != nil {
				t.Fatalf("Config() returned error: %v", err)
			}
			after := before.Clone()
			after.JiraBaseURL = tt.base

			tx, err := st.Begin(ctx, ActorHuman)
			if err != nil {
				t.Fatalf("Begin() returned error: %v", err)
			}
			defer tx.Rollback()

			if _, err := tx.SaveConfig(ctx, before, after); err == nil {
				t.Errorf("SaveConfig() accepted %q", tt.base)
			}
		})
	}
}

func TestSaveConfigAcceptsClearingASetting(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	before, err := st.Config(ctx)
	if err != nil {
		t.Fatalf("Config() returned error: %v", err)
	}
	after := before.Clone()
	after.JiraBaseURL = ""

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.SaveConfig(ctx, before, after); err != nil {
		t.Errorf("SaveConfig() refused an empty base URL: %v", err)
	}
}

func TestSetPrefixes(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{"none", nil, "[]"},
		{"one", []string{"CDSS"}, `["CDSS"]`},
		{"upper cased", []string{"cdss"}, `["CDSS"]`},
		{"trimmed", []string{"  CDSS  "}, `["CDSS"]`},
		{"de-duplicated", []string{"CDSS", "cdss"}, `["CDSS"]`},
		{"order kept", []string{"PLAT", "CDSS"}, `["PLAT","CDSS"]`},
		{"empty entries dropped", []string{""}, "[]"},
		{"digits after the first character", []string{"S3"}, `["S3"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c Config
			if err := c.SetPrefixes(tt.in); err != nil {
				t.Fatalf("SetPrefixes(%v) returned error: %v", tt.in, err)
			}
			if c.JiraPrefixes != tt.want {
				t.Errorf("SetPrefixes(%v) stored %s, want %s", tt.in, c.JiraPrefixes, tt.want)
			}
		})
	}
}

// TestSetPrefixesRejectsWhatCouldNeverMatch: a prefix the linker's pattern
// cannot produce would sit in the configuration linking nothing, with no
// error anywhere to explain why.
func TestSetPrefixesRejectsWhatCouldNeverMatch(t *testing.T) {
	for _, in := range []string{"C", "3D", "CD-SS", "CD SS"} {
		var c Config
		if err := c.SetPrefixes([]string{in}); err == nil {
			t.Errorf("SetPrefixes(%q) = nil, want an error", in)
		}
	}
}

// TestPrefixesRoundTrip: what SetPrefixes writes is what Prefixes reads, so
// the page links exactly the keys that were configured.
func TestPrefixesRoundTrip(t *testing.T) {
	var c Config
	if err := c.SetPrefixes([]string{"cdss", "PLAT"}); err != nil {
		t.Fatalf("SetPrefixes() returned error: %v", err)
	}
	got, err := c.Prefixes()
	if err != nil {
		t.Fatalf("Prefixes() returned error: %v", err)
	}
	want := []string{"CDSS", "PLAT"}
	if len(got) != len(want) {
		t.Fatalf("Prefixes() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Prefixes()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestPrefixesReportsABadColumn: json_valid() in the schema allows a JSON
// number as readily as an array, so the decode is the thing that catches it.
func TestPrefixesReportsABadColumn(t *testing.T) {
	c := Config{JiraPrefixes: `{"CDSS":true}`}
	_, err := c.Prefixes()
	if err == nil {
		t.Fatal("Prefixes() accepted an object, want an error")
	}
	if !strings.Contains(err.Error(), "jira_prefixes") {
		t.Errorf("error = %v, want it to name the column", err)
	}
}

// TestConfigIsNotSyncsToWrite: nothing about how the page is addressed is
// observed from anywhere, so the authored rule should hold here too.
func TestConfigIsNotSyncsToWrite(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	before, err := st.Config(ctx)
	if err != nil {
		t.Fatalf("Config() returned error: %v", err)
	}
	after := before.Clone()
	after.Owner = "sync said so"

	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.SaveConfig(ctx, before, after); err == nil {
		t.Error("SaveConfig() let sync write an authored column")
	}
}
