package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// repoSeparator divides an owner from a repository name, matching the
// schema's CHECK (id = owner || '/' || name).
const repoSeparator = "/"

// GitHubRepo is a tracked repository.
//
// It exists because a repository carries policy a pull request cannot. Chief
// among them is Pipeline: what happens to a pull request between written and
// merged, which is not the same everywhere.
//
// Pipeline is authored rather than observed, which cuts against the usual
// rule for anything GitHub knows. Reading branch protection needs admin on
// the repository, so it is unavailable exactly where the repository is not
// yours — and "these repositories do not get reviewed" is a statement about
// how someone works, not a fact retrieved from an API. Making it observed
// would let a sync overwrite it with a shrug.
type GitHubRepo struct {
	ID    string `db:"id" kind:"identity"`
	Owner string `db:"owner" kind:"identity"`
	Name  string `db:"name" kind:"identity"`

	AnnounceChannel sql.NullString `db:"announce_channel"`
	Disposition     string         `db:"disposition"`

	DefaultBranch  sql.NullString `db:"default_branch" kind:"observed"`
	UsesMergeQueue sql.NullBool   `db:"uses_merge_queue" kind:"observed"`
	IsArchived     sql.NullBool   `db:"is_archived" kind:"observed"`
	Raw            string         `db:"raw" kind:"observed" format:"json"`

	TrackedSince string         `db:"tracked_since" kind:"created"`
	LastSyncedAt sql.NullString `db:"last_synced_at" kind:"observed"`

	// Pipeline is last because ADD COLUMN put the column there. NULL means
	// unstated: a repository tracked before anyone said how its pull requests
	// get merged.
	Pipeline sql.NullString `db:"pipeline"`
}

func (r *GitHubRepo) table() string       { return "github_repo" }
func (r *GitHubRepo) subjectType() string { return "github_repo" }
func (r *GitHubRepo) subjectID() string   { return r.ID }

// RepoID builds the identifier for an owner and name.
func RepoID(owner, name string) string {
	return owner + repoSeparator + name
}

// ParseRepoID splits an identifier of the form owner/name.
//
// Both halves must be present and neither may contain a further separator,
// or the identifier would not split back to what built it — something the
// schema's CHECK cannot catch, since concatenation succeeds either way.
func ParseRepoID(id string) (owner, name string, err error) {
	owner, name, found := strings.Cut(id, repoSeparator)
	switch {
	case !found:
		return "", "", fmt.Errorf("%q is not a repository: expected owner%sname", id, repoSeparator)
	case owner == "":
		return "", "", fmt.Errorf("%q has no owner", id)
	case name == "":
		return "", "", fmt.Errorf("%q has no repository name", id)
	case strings.Contains(name, repoSeparator):
		return "", "", fmt.Errorf("%q has more than one %s", id, repoSeparator)
	case strings.Contains(id, prSeparator):
		return "", "", fmt.Errorf("%q looks like a pull request key, not a repository", id)
	}
	return owner, name, nil
}

// NewGitHubRepo returns an unsaved repository. Everything but the key is
// either the user's policy or sync's to observe.
func NewGitHubRepo(owner, name string) *GitHubRepo {
	return &GitHubRepo{
		ID:    RepoID(owner, name),
		Owner: owner,
		Name:  name,
		Raw:   "{}",
	}
}

// LoadGitHubRepo reads a repository by id, returning sql.ErrNoRows if it is
// not tracked.
func (t *Tx) LoadGitHubRepo(ctx context.Context, id string) (*GitHubRepo, error) {
	var r GitHubRepo
	if err := t.Load(ctx, &r, id); err != nil {
		return nil, err
	}
	return &r, nil
}

// Clone returns a copy to mutate, leaving the original as the before image
// for Tx.Update.
func (r *GitHubRepo) Clone() *GitHubRepo {
	clone := *r
	return &clone
}

// ListGitHubRepos returns tracked repositories, ordered by owner then name.
func (s *Store) ListGitHubRepos(ctx context.Context) ([]*GitHubRepo, error) {
	fields, err := fieldsOfStruct(&GitHubRepo{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	query := fmt.Sprintf("SELECT %s FROM github_repo ORDER BY owner, name", strings.Join(columns, ", "))
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing repositories: %w", err)
	}
	defer rows.Close()

	var repos []*GitHubRepo
	for rows.Next() {
		var r GitHubRepo
		dest := make([]any, len(fields))
		for i, f := range fields {
			dest[i] = f.pointerOf(&r)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("listing repositories: %w", err)
		}
		repos = append(repos, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing repositories: %w", err)
	}
	return repos, nil
}
