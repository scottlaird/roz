package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

const (
	flagSprint   = "sprint"
	flagAssignee = "assignee"
	flagFeed     = "feed"
)

func newProjectJiraCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jira [issue-key]",
		Short: "Record by hand what Jira says about a project",
		Long: "Stands in for Jira sync, which does not exist yet.\n\n" +
			"Keyed on the issue rather than on the project, because that is what an\n" +
			"integration would have: a Jira issue does not know it is SL106. Every\n" +
			"project carrying the key is updated, and a key no project carries is\n" +
			"reported rather than treated as an error — this tracks a subset of\n" +
			"what Jira holds.\n\n" +
			"--feed takes the same thing in bulk, as JSON, so faking a sync run is\n" +
			"one command:\n\n" +
			"  todo project jira --feed - <<'JSON'\n" +
			"  [{\"key\":\"CDSS-1744\",\"status\":\"In Progress\",\"assignee\":\"scott\"}]\n" +
			"  JSON\n\n" +
			"A field not given is left alone, because absence is not a fact. A field\n" +
			"given as empty is a fact — an unassigned issue — and clears the column.\n\n" +
			"This writes observed columns, which a person is normally not allowed to\n" +
			"do. It is logged as sync:jira-manual so the log never claims Jira\n" +
			"reported something that was typed in.",
		Args: cobra.MaximumNArgs(1),
		RunE: runProjectJira,
	}
	f := cmd.Flags()
	f.String(flagStatus, "", "Jira's status, e.g. In Progress; Jira's vocabulary, not ours")
	f.String(flagSprint, "", "the sprint it is in")
	f.String(flagAssignee, "", "who it is assigned to; empty means unassigned")
	f.String(flagAt, "", "when Jira was read, as a date or timestamp; defaults to now")
	f.String(flagFeed, "", "read observations as JSON from a file, or - for stdin")
	return cmd
}

func runProjectJira(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	observations, err := jiraObservations(cmd, args)
	if err != nil {
		return err
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	// The actor is fixed by the command rather than taken from a flag: this
	// is one of two places a person may write observed columns, and it should
	// be reachable only on purpose.
	result, err := st.ObserveJira(ctx, store.ActorJiraManual, observations)
	if err != nil {
		return err
	}
	return writeJiraResult(cmd.OutOrStdout(), result)
}

// jiraObservations reads either the one on the command line or the feed.
func jiraObservations(cmd *cobra.Command, args []string) ([]store.JiraObservation, error) {
	f := cmd.Flags()

	feed, err := f.GetString(flagFeed)
	if err != nil {
		return nil, err
	}
	if feed != "" {
		if len(args) > 0 {
			return nil, fmt.Errorf("--%s reads the issue keys itself; drop the %s argument",
				flagFeed, args[0])
		}
		return readJiraFeed(cmd, feed)
	}

	if len(args) != 1 {
		return nil, fmt.Errorf("name an issue key, or pass --%s", flagFeed)
	}

	at, err := f.GetString(flagAt)
	if err != nil {
		return nil, err
	}
	if at != "" {
		if at, err = validateTimestamp(flagAt, at); err != nil {
			return nil, err
		}
	}

	observation := store.JiraObservation{Key: args[0], SyncedAt: at}
	for _, field := range []struct {
		flag   string
		target *sql.NullString
	}{
		{flagStatus, &observation.Status},
		{flagSprint, &observation.Sprint},
		{flagAssignee, &observation.Assignee},
	} {
		if !f.Changed(field.flag) {
			continue // not reported, so not a fact about anything
		}
		value, err := f.GetString(field.flag)
		if err != nil {
			return nil, err
		}
		*field.target = sql.NullString{String: value, Valid: true}
	}

	if !observation.Status.Valid && !observation.Sprint.Valid && !observation.Assignee.Valid {
		return nil, fmt.Errorf("nothing observed: pass --%s, --%s or --%s",
			flagStatus, flagSprint, flagAssignee)
	}
	return []store.JiraObservation{observation}, nil
}

// jiraFeedEntry is one observation as JSON.
//
// The fields are pointers so that a key left out of the object is
// distinguishable from one present and empty, which is the same distinction
// the flags make by being given or not.
type jiraFeedEntry struct {
	Key      string  `json:"key"`
	Status   *string `json:"status"`
	Sprint   *string `json:"sprint"`
	Assignee *string `json:"assignee"`
	SyncedAt string  `json:"synced_at"`
}

func readJiraFeed(cmd *cobra.Command, path string) ([]store.JiraObservation, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(cmd.InOrStdin())
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the feed: %w", err)
	}

	entries, err := decodeJiraFeed(raw)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("the feed holds no observations")
	}

	observations := make([]store.JiraObservation, len(entries))
	for i, entry := range entries {
		if entry.Key == "" {
			return nil, fmt.Errorf("observation %d has no key", i+1)
		}
		if entry.SyncedAt != "" {
			if _, err := validateTimestamp("synced_at", entry.SyncedAt); err != nil {
				return nil, fmt.Errorf("%s: %w", entry.Key, err)
			}
		}
		observations[i] = store.JiraObservation{
			Key:      entry.Key,
			Status:   optional(entry.Status),
			Sprint:   optional(entry.Sprint),
			Assignee: optional(entry.Assignee),
			SyncedAt: entry.SyncedAt,
		}
	}
	return observations, nil
}

// decodeJiraFeed accepts an array or a single object, since one observation
// is the common case and wrapping it in brackets is friction.
func decodeJiraFeed(raw []byte) ([]jiraFeedEntry, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "{") {
		var entry jiraFeedEntry
		if err := json.Unmarshal([]byte(trimmed), &entry); err != nil {
			return nil, fmt.Errorf("the feed is not valid JSON: %w", err)
		}
		return []jiraFeedEntry{entry}, nil
	}

	var entries []jiraFeedEntry
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		return nil, fmt.Errorf("the feed is not valid JSON: %w", err)
	}
	return entries, nil
}

func optional(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func writeJiraResult(out io.Writer, result *store.JiraResult) error {
	for _, applied := range result.Applied {
		if len(applied.Changes) == 0 {
			fmt.Fprintf(out, "%s unchanged\n", applied.ProjectID)
			continue
		}
		for _, change := range applied.Changes {
			fmt.Fprintf(out, "%s %s\n", applied.ProjectID, change)
		}
	}
	for _, key := range result.Unmatched {
		fmt.Fprintf(out, "%s matches no project\n", key)
	}
	return nil
}
