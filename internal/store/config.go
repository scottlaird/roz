package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ConfigID is the only key the config table accepts, and the subject id every
// settings event carries. There is one row by construction: the CHECK in the
// schema says so, and the migration seeds it.
const ConfigID = "config"

// Config is the settings the database carries rather than the command line.
//
// The test for belonging here is whether a value is a property of this queue
// rather than of one invocation: the Jira host does not change between two
// commands run a minute apart, and having to say so twice is how it ends up
// said differently. Which database to open is the counter-example, and stays
// a flag — it cannot be read out of a database that has not been chosen yet.
//
// Every column is authored. Nothing here is observed: sync learns facts about
// pull requests, not about how the page should be addressed.
type Config struct {
	ID string `db:"id" kind:"identity"`

	Owner        string `db:"owner"`
	JiraBaseURL  string `db:"jira_base_url"`
	JiraPrefixes string `db:"jira_prefixes" format:"json"` // JSON array of project keys

	CreatedAt string `db:"created_at" kind:"created"`
	UpdatedAt string `db:"updated_at" kind:"auto"`

	// PollWindowDays is how long after a pull request ends to keep asking
	// GitHub about it. Zero polls only what is still open.
	//
	// A property of this queue rather than of one invocation, which is the
	// test for belonging here: how far back to look does not change between
	// two commands run a minute apart.
	PollWindowDays int64 `db:"poll_window_days"`

	// WeekStart is the day a week is labelled from — what "Week of ..." means
	// in a heading, and nothing else. ReviewDay is the day the review is
	// actually done, which decides the window: the seven days ending at it.
	//
	// Two settings rather than one because they answer different questions and
	// routinely disagree — reviewing on a Friday while thinking of weeks as
	// starting on Monday is ordinary. See WeekOf for what falls out of that.
	//
	// ReviewDay is a window definition and not a schedule. Nothing fires on
	// it; the report is run when somebody runs it.
	WeekStart string `db:"week_start"`
	ReviewDay string `db:"review_day"`
}

func (c *Config) table() string       { return "config" }
func (c *Config) subjectType() string { return "config" }
func (c *Config) subjectID() string   { return c.ID }

// Clone returns a copy to mutate, leaving the original as the before image
// for Tx.Update.
func (c *Config) Clone() *Config {
	clone := *c
	return &clone
}

// Config reads the settings.
//
// It reads them afresh each time rather than caching on the Store, because
// `roz serve` outlives a `roz config set` in another terminal and should
// render the page the way it is configured now.
func (s *Store) Config(ctx context.Context) (*Config, error) {
	return scanConfig(ctx, s.db)
}

// LoadConfig reads the settings inside a unit of work, which is what a caller
// about to change them wants: the before image and the update are then the
// same transaction.
func (t *Tx) LoadConfig(ctx context.Context) (*Config, error) {
	return scanConfig(ctx, t.tx)
}

// queryRower is the part of *sql.DB and *sql.Tx this file needs.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func scanConfig(ctx context.Context, q queryRower) (*Config, error) {
	var c Config
	fields, err := fieldsOfStruct(&c)
	if err != nil {
		return nil, err
	}

	columns := make([]string, len(fields))
	dest := make([]any, len(fields))
	for i, f := range fields {
		columns[i] = f.column
		dest[i] = f.pointerOf(&c)
	}

	query := fmt.Sprintf("SELECT %s FROM config WHERE id = ?", strings.Join(columns, ", "))
	switch err := q.QueryRowContext(ctx, query, ConfigID).Scan(dest...); {
	case errors.Is(err, sql.ErrNoRows):
		// The migration seeds the row, so this means someone deleted it.
		// Saying that is more use than an empty struct that reads as "nothing
		// is configured".
		return nil, fmt.Errorf("the config row is missing; the database has been modified by hand")
	case err != nil:
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return &c, nil
}

// SaveConfig writes changed settings, validating them first, and logs one
// event per column that moved.
//
// Validation lives here rather than in the command for the same reason the
// field kinds do: this is the only path to the row, so the CLI and the MCP
// server cannot disagree about what a usable base URL is.
func (t *Tx) SaveConfig(ctx context.Context, before, after *Config) ([]Change, error) {
	if err := after.Validate(); err != nil {
		return nil, err
	}
	return t.Update(ctx, before, after)
}

// Validate checks the settings are usable.
//
// Empty is always allowed. Unset means "do not link", which is the honest
// state for a queue that has nothing to do with Jira, and refusing it would
// make the feature mandatory.
func (c *Config) Validate() error {
	if err := ValidateJiraBaseURL(c.JiraBaseURL); err != nil {
		return err
	}
	prefixes, err := c.Prefixes()
	if err != nil {
		return err
	}
	for _, p := range prefixes {
		if err := ValidateJiraPrefix(p); err != nil {
			return err
		}
	}
	return nil
}

// ValidateJiraBaseURL checks that a key appended to the base would produce a
// usable link.
//
// A relative or scheme-less value is refused rather than repaired: it renders
// as a link that looks right and resolves against whatever host the page
// happens to be served from, which is the failure that is hardest to notice.
func ValidateJiraBaseURL(base string) error {
	if base == "" {
		return nil
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("jira base URL %q is not a URL: %w", base, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("jira base URL %q needs an http or https scheme, "+
			"e.g. https://example.atlassian.net/browse", base)
	}
	if parsed.Host == "" {
		return fmt.Errorf("jira base URL %q has no host", base)
	}
	return nil
}

// jiraPrefixPattern is the project half of the key pattern the linker
// matches, anchored: a prefix that could never appear in a key would silently
// link nothing.
var jiraPrefixPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]+$`)

// ValidateJiraPrefix checks a project key prefix, e.g. CDSS.
func ValidateJiraPrefix(prefix string) error {
	if !jiraPrefixPattern.MatchString(prefix) {
		return fmt.Errorf("jira prefix %q is not a project key: "+
			"two or more characters, upper case, starting with a letter, e.g. CDSS", prefix)
	}
	return nil
}

// Prefixes decodes the project keys worth linking.
func (c *Config) Prefixes() ([]string, error) {
	if c.JiraPrefixes == "" {
		return nil, nil
	}
	var prefixes []string
	if err := json.Unmarshal([]byte(c.JiraPrefixes), &prefixes); err != nil {
		return nil, fmt.Errorf("config.jira_prefixes is not a JSON array of strings: %w", err)
	}
	return prefixes, nil
}

// SetPrefixes replaces the project keys worth linking.
//
// Keys are upper-cased and de-duplicated, and the order given is kept:
// nothing reads the list in order, but reordering it on the way in would show
// up in the log as a change nobody made.
func (c *Config) SetPrefixes(prefixes []string) error {
	seen := make(map[string]bool, len(prefixes))
	cleaned := []string{}
	for _, p := range prefixes {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		if err := ValidateJiraPrefix(p); err != nil {
			return err
		}
		seen[p] = true
		cleaned = append(cleaned, p)
	}

	encoded, err := json.Marshal(cleaned)
	if err != nil {
		return fmt.Errorf("encoding jira prefixes: %w", err)
	}
	c.JiraPrefixes = string(encoded)
	return nil
}
