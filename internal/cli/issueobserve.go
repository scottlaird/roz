package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

const (
	flagIteration = "iteration"
	flagSprint    = "sprint"
	flagAssignee  = "assignee"
	flagFeed      = "feed"
	flagTracker   = "tracker"
	flagKey       = "key"
	// flagClosedAt is separate from --status because it is a different fact.
	// A status is where the issue is; this is when it stopped moving, and it
	// is what `issue list --since` reads. GitHub reports it on every sync;
	// for a tracker nothing reads, this flag is the only way it arrives.
	flagClosedAt = "closed-at"
)

func newIssueObserveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "observe [issue-key]",
		Short: "Record by hand what a tracker says about an issue",
		Long: "Stands in for tracker sync, which exists for GitHub and no other\n" +
			"tracker: `roz sync github` reads the issues projects track, and a Jira\n" +
			"one gets here or not at all.\n\n" +
			"Keyed on the issue rather than on the project, because that is what an\n" +
			"integration would have: a Jira issue does not know it is ROZ106. Every\n" +
			"project carrying the key is updated, and a key no project carries is\n" +
			"reported rather than treated as an error — this tracks a subset of\n" +
			"what the tracker holds.\n\n" +
			"--feed takes the same thing in bulk, as JSON, so faking a sync run is\n" +
			"one command:\n\n" +
			"  roz issue observe --feed - <<'JSON'\n" +
			"  [{\"key\":\"CDSS-1744\",\"status\":\"In Progress\",\"assignee\":\"scott\"}]\n" +
			"  JSON\n\n" +
			"An entry may name its own tracker; without one it takes --tracker.\n\n" +
			"A field not given is left alone, because absence is not a fact. A field\n" +
			"given as empty is a fact — an unassigned issue — and clears the column.\n\n" +
			"This writes observed columns, which a person is normally not allowed to\n" +
			"do. It is logged as sync:<tracker>-manual so the log never claims a\n" +
			"tracker reported something that was typed in.\n\n" +
			"An observation that finds nothing changed writes nothing, so that a\n" +
			"poll cannot bury real transitions under a heartbeat. Pass --at to\n" +
			"record when the tracker was read regardless: a stated time is a fact\n" +
			"being asserted rather than a reading that found nothing.",
		Args: cobra.MaximumNArgs(1),
		RunE: runIssueObserve,
	}
	addObserveFlags(cmd)
	return cmd
}

func addObserveFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	addTrackerFlag(cmd)
	f.String("summary", "", "the issue's title, so a key is legible without opening the tracker")
	f.String(flagStatus, "", "the tracker's status, e.g. In Progress; its vocabulary, not ours")
	f.String(flagIteration, "", "the sprint or milestone it is in")
	f.String(flagSprint, "", "the sprint it is in")
	_ = f.MarkDeprecated(flagSprint, "use --iteration, which covers a milestone too")
	f.String(flagAssignee, "", "who it is assigned to; empty means unassigned")
	f.String(flagClosedAt, "", "when the tracker says it closed, as a date or timestamp")
	f.String(flagAt, "", "when the tracker was read, as a date or timestamp; recorded even if nothing else changed")
	f.String(flagFeed, "", "read observations as JSON from a file, or - for stdin")
}

// addTrackerFlag registers --tracker where a command needs one.
//
// Defaulted rather than required, because jira is the only tracker anything
// can read and every issue recorded so far is one. The default is a statement
// about today, and worth revisiting the moment a second tracker works.
func addTrackerFlag(cmd *cobra.Command) {
	cmd.Flags().String(flagTracker, store.TrackerJira,
		"which tracker the key belongs to: "+strings.Join(store.Trackers, " or "))
}

func runIssueObserve(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	tracker, err := cmd.Flags().GetString(flagTracker)
	if err != nil {
		return err
	}
	if err := store.ValidateTracker(tracker); err != nil {
		return err
	}

	observations, err := issueObservations(cmd, args, tracker)
	if err != nil {
		return err
	}

	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	// The actor is fixed by the command rather than taken from a flag: this
	// is one of two places a person may write observed columns, and it should
	// be reachable only on purpose. One actor per tracker, so the log never
	// says jira about something read from GitHub.
	actor, err := store.ManualActorFor(tracker)
	if err != nil {
		return err
	}
	result, err := st.ObserveTrackerIssues(ctx, actor, observations)
	if err != nil {
		return err
	}
	if err := writeObserveResult(cmd.OutOrStdout(), result); err != nil {
		return err
	}

	// An issue closing is exactly what a wait_issue action closes on, and it
	// has just arrived. Without this the step sits ready with nothing left to
	// wait for until the next sync — the same staleness `pr announce` settles
	// for the same reason, and under the same actor: recording what a tracker
	// said is an observation, and deciding a wait is therefore over is a rule
	// somebody wrote into the vocabulary.
	return settleNow(ctx, cmd, st)
}

// issueObservations reads either the one on the command line or the feed.
func issueObservations(cmd *cobra.Command, args []string, tracker string) ([]store.TrackerObservation, error) {
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
		return readIssueFeed(cmd, feed, tracker)
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

	closedAt, err := f.GetString(flagClosedAt)
	if err != nil {
		return nil, err
	}
	if closedAt != "" {
		if closedAt, err = validateTimestamp(flagClosedAt, closedAt); err != nil {
			return nil, err
		}
	}

	observation := store.TrackerObservation{Tracker: tracker, Key: args[0], SyncedAt: at}
	if f.Changed(flagClosedAt) {
		observation.ClosedAt = sql.NullString{String: closedAt, Valid: true}
	}
	for _, field := range []struct {
		flag   string
		target *sql.NullString
	}{
		{"summary", &observation.Summary},
		{flagStatus, &observation.Status},
		{flagIteration, &observation.Iteration},
		{flagSprint, &observation.Iteration},
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

	if !observation.Summary.Valid && !observation.Status.Valid &&
		!observation.Iteration.Valid && !observation.Assignee.Valid &&
		!observation.ClosedAt.Valid {
		return nil, fmt.Errorf("nothing observed: pass --summary, --%s, --%s, --%s or --%s",
			flagStatus, flagIteration, flagAssignee, flagClosedAt)
	}
	return []store.TrackerObservation{observation}, nil
}

// jiraFeedEntry is one observation as JSON.
//
// The fields are pointers so that a key left out of the object is
// distinguishable from one present and empty, which is the same distinction
// the flags make by being given or not.
type issueFeedEntry struct {
	// Tracker is optional; empty takes --tracker. A feed from one integration
	// is all one tracker, so repeating it on every entry would be noise.
	Tracker string  `json:"tracker"`
	Key     string  `json:"key"`
	Summary *string `json:"summary"`
	Status  *string `json:"status"`
	// Sprint is accepted as the old name for Iteration, so a feed written
	// against the Jira-only shape still loads.
	Iteration *string `json:"iteration"`
	Sprint    *string `json:"sprint"`
	Assignee  *string `json:"assignee"`
	ClosedAt  *string `json:"closed_at"`
	SyncedAt  string  `json:"synced_at"`
}

// iteration prefers the neutral name and falls back to the old one.
func (e issueFeedEntry) iteration() *string {
	if e.Iteration != nil {
		return e.Iteration
	}
	return e.Sprint
}

func readIssueFeed(cmd *cobra.Command, path, tracker string) ([]store.TrackerObservation, error) {
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

	entries, err := decodeIssueFeed(raw)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("the feed holds no observations")
	}

	observations := make([]store.TrackerObservation, len(entries))
	for i, entry := range entries {
		if entry.Key == "" {
			return nil, fmt.Errorf("observation %d has no key", i+1)
		}
		entryTracker := entry.Tracker
		if entryTracker == "" {
			entryTracker = tracker
		}
		if err := store.ValidateTracker(entryTracker); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Key, err)
		}
		if entry.SyncedAt != "" {
			if _, err := validateTimestamp("synced_at", entry.SyncedAt); err != nil {
				return nil, fmt.Errorf("%s: %w", entry.Key, err)
			}
		}
		if entry.ClosedAt != nil && *entry.ClosedAt != "" {
			if _, err := validateTimestamp("closed_at", *entry.ClosedAt); err != nil {
				return nil, fmt.Errorf("%s: %w", entry.Key, err)
			}
		}
		observations[i] = store.TrackerObservation{
			Tracker:   entryTracker,
			Key:       entry.Key,
			Summary:   optional(entry.Summary),
			Status:    optional(entry.Status),
			Iteration: optional(entry.iteration()),
			Assignee:  optional(entry.Assignee),
			ClosedAt:  optional(entry.ClosedAt),
			SyncedAt:  entry.SyncedAt,
		}
	}
	return observations, nil
}

// decodeIssueFeed accepts an array or a single object, since one observation
// is the common case and wrapping it in brackets is friction.
func decodeIssueFeed(raw []byte) ([]issueFeedEntry, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "{") {
		var entry issueFeedEntry
		if err := json.Unmarshal([]byte(trimmed), &entry); err != nil {
			return nil, fmt.Errorf("the feed is not valid JSON: %w", err)
		}
		return []issueFeedEntry{entry}, nil
	}

	var entries []issueFeedEntry
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

// writeObserveResult reports per issue, and says which projects track it.
//
// An issue nothing tracks is reported rather than warned about: it is a record
// in its own right, so storing what the tracker said about it is the right
// outcome, not a miss. What is worth remarking on is that nobody is watching.
func writeObserveResult(out io.Writer, result *store.TrackerResult) error {
	for _, applied := range result.Applied {
		id := applied.ID()
		switch {
		case applied.Created:
			fmt.Fprintf(out, "%s recorded\n", id)
		case len(applied.Changes) == 0:
			fmt.Fprintf(out, "%s unchanged\n", id)
		}
		for _, change := range applied.Changes {
			fmt.Fprintf(out, "%s %s\n", id, change)
		}
		if len(applied.Projects) == 0 {
			fmt.Fprintf(out, "%s is tracked by no project\n", id)
		}
	}
	return nil
}
