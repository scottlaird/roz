package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// OwnerChannel is where a group is reached.
//
// The channel belongs to the reviewer rather than to the repository, which is
// the whole point: a change touching storage should reach the storage channel
// whichever repository it is in, and a monorepo has no single right answer at
// all.
//
// Teams only. A channel is how you reach a group; an individual is reached by
// naming them, and giving a person a channel is a different concept wearing
// the same word.
type OwnerChannel struct {
	Owner   string `db:"owner" kind:"identity"`
	Channel string `db:"channel"`

	CreatedAt string `db:"created_at" kind:"created"`
	UpdatedAt string `db:"updated_at" kind:"auto"`
}

func (c *OwnerChannel) table() string       { return "owner_channel" }
func (c *OwnerChannel) subjectType() string { return "owner_channel" }
func (c *OwnerChannel) subjectID() string   { return c.Owner }

// keyColumn: keyed on the owner, not on a surrogate id. A team already spells
// its organisation, so the name is unique without one.
func (c *OwnerChannel) keyColumn() string { return "owner" }

func (c *OwnerChannel) Clone() *OwnerChannel {
	clone := *c
	return &clone
}

// ValidateOwnerChannel refuses what the table cannot hold, before a write
// reaches a CHECK constraint that can only say the row is bad.
func ValidateOwnerChannel(owner, channel string) error {
	if !strings.Contains(owner, "/") {
		return fmt.Errorf("%s is a person, not a group: a channel is how you reach a group, and a person is reached by naming them", owner)
	}
	if strings.TrimSpace(channel) == "" {
		return fmt.Errorf("a channel for %s cannot be empty", owner)
	}
	return nil
}

// LoadOwnerChannel reads one owner's channel, sql.ErrNoRows when it has none.
func (t *Tx) LoadOwnerChannel(ctx context.Context, owner string) (*OwnerChannel, error) {
	c := &OwnerChannel{Owner: owner}
	if err := t.Load(ctx, c, owner); err != nil {
		return nil, err
	}
	return c, nil
}

// SetOwnerChannel records where a group is reached, creating the row the first
// time.
func (s *Store) SetOwnerChannel(ctx context.Context, actor Actor, owner, channel string) ([]Change, error) {
	if err := ValidateOwnerChannel(owner, channel); err != nil {
		return nil, err
	}

	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var changes []Change
	before, err := tx.LoadOwnerChannel(ctx, owner)
	switch {
	case err == sql.ErrNoRows:
		if err := tx.Insert(ctx, &OwnerChannel{Owner: owner, Channel: channel}); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		after := before.Clone()
		after.Channel = channel
		if changes, err = tx.Update(ctx, before, after); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return changes, nil
}

// ClearOwnerChannel forgets where a group is reached.
func (s *Store) ClearOwnerChannel(ctx context.Context, actor Actor, owner string) error {
	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	before, err := tx.LoadOwnerChannel(ctx, owner)
	if err != nil {
		return err
	}
	if _, err := tx.tx.ExecContext(ctx,
		"DELETE FROM owner_channel WHERE owner = ?", owner); err != nil {
		return fmt.Errorf("clearing the channel for %s: %w", owner, err)
	}
	if err := tx.emit(ctx, before, event{
		kind: eventChanged, field: "channel", oldValue: before.Channel, newValue: "",
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// OwnerChannels returns every recorded channel, by owner.
func (s *Store) OwnerChannels(ctx context.Context, filter SQLWhere) ([]*OwnerChannel, error) {
	fields, err := fieldsOf(&OwnerChannel{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	where, args := filter.clause(nil, nil)
	query := fmt.Sprintf("SELECT %s FROM owner_channel", strings.Join(columns, ", "))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY owner"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading owner channels: %w", err)
	}
	defer rows.Close()

	var all []*OwnerChannel
	for rows.Next() {
		var c OwnerChannel
		dest := make([]any, len(fields))
		for i, f := range fields {
			dest[i] = f.pointerOf(&c)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("reading owner channels: %w", err)
		}
		copied := c
		all = append(all, &copied)
	}
	return all, rows.Err()
}

// Why an announcement went where it did.
const (
	// ChannelFromWait: somebody said which owner this pull request is waiting
	// for, which is the most direct answer there is — it is the judgement the
	// routing is trying to reconstruct.
	ChannelFromWait = "the owner this is waiting for"
	// ChannelFromHint: the repository prefers this owner, and this pull
	// request requires it.
	ChannelFromHint = "preferred by the repository, and required here"
	// ChannelFromRequired: no preference applied, so the owners the change
	// needs answer for themselves.
	ChannelFromRequired = "required by CODEOWNERS"
	// ChannelFromRepo: the repository's own channel, which is the wrong grain
	// and is therefore the last resort rather than the default.
	ChannelFromRepo = "the repository's own channel; no owner of this change has one"
)

// AnnounceTarget is where a review request should go, and why.
type AnnounceTarget struct {
	Channel string
	// Owner is whose channel it is, empty where the repository supplied it.
	Owner string
	// Why is one of the constants above.
	Why string
}

// ChannelFor works out where a pull request's review request should go.
//
// A lookup rather than a rules engine. Deciding who to ask is #136's job and
// needs the files, the CODEOWNERS on the base branch and team membership;
// this reads what that decision already left behind — the owners the change
// requires, the repository's preference among them, and whichever owner
// somebody has named as the one being waited for.
//
// Ordering follows the routing it stands in for: a preference is tried first
// but only where it is one of the owners this change actually needs, so a hint
// that has nothing to do with this pull request is skipped rather than
// announced to. What is left is taken in a stable order, because an
// announcement that moved between two equally good channels from one run to
// the next would be worse than one that is merely arbitrary.
//
// An owner with no channel is not an error and not a stopping point: it is
// passed over, and if nothing has one the caller is told which owners were
// considered. A person never has one — that is what the table's key means —
// so a change owned by an individual falls through to the repository, and to
// --channel after that.
func (s *Store) ChannelFor(ctx context.Context, prKey string) (*AnnounceTarget, []string, error) {
	tx, err := s.Begin(ctx, ActorHuman)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	pr, err := tx.LoadPR(ctx, prKey)
	if err != nil {
		return nil, nil, err
	}

	candidates, err := tx.announceCandidates(ctx, pr)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range candidates {
		channel, err := tx.LoadOwnerChannel(ctx, c.owner)
		switch {
		case err == sql.ErrNoRows:
			continue
		case err != nil:
			return nil, nil, err
		}
		return &AnnounceTarget{Channel: channel.Channel, Owner: c.owner, Why: c.why}, nil, nil
	}

	considered := make([]string, 0, len(candidates))
	for _, c := range candidates {
		considered = append(considered, c.owner)
	}

	// The repository's own channel, which predates knowing who the reviewers
	// are. Kept as a last resort rather than removed: plenty of repositories
	// do have one right answer, and one that is announced by habit is better
	// recorded than retyped. It is last because the grain is wrong — it cannot
	// distinguish a storage change from a docs one.
	repo, err := tx.LoadGitHubRepo(ctx, pr.Repo)
	switch {
	case err == sql.ErrNoRows:
	case err != nil:
		return nil, nil, err
	default:
		if repo.AnnounceChannel.Valid && repo.AnnounceChannel.String != "" {
			return &AnnounceTarget{Channel: repo.AnnounceChannel.String, Why: ChannelFromRepo}, considered, nil
		}
	}
	return nil, considered, nil
}

// announceCandidate is one owner worth asking about, and why it came up.
type announceCandidate struct {
	owner string
	why   string
}

// announceCandidates orders the owners whose channel would be the right one.
func (t *Tx) announceCandidates(ctx context.Context, pr *PR) ([]announceCandidate, error) {
	var ordered []announceCandidate
	seen := map[string]bool{}
	add := func(owner, why string) {
		if owner == "" || seen[owner] {
			return
		}
		seen[owner] = true
		ordered = append(ordered, announceCandidate{owner: owner, why: why})
	}

	// The authored answer first. Where somebody has said which owner this is
	// waiting for, that is the owner actually asked, and no amount of reading
	// CODEOWNERS improves on it.
	waitingFor, err := t.waitingForOwners(ctx, pr.ID)
	if err != nil {
		return nil, err
	}
	for _, owner := range waitingFor {
		add(owner, ChannelFromWait)
	}

	required, err := decodeOwners(pr.RequiredOwners)
	if err != nil {
		return nil, err
	}
	isRequired := make(map[string]bool, len(required))
	for _, owner := range required {
		isRequired[owner] = true
	}

	// A preference, checked rather than trusted, exactly as routing checks it:
	// a hinted owner that this change does not need is skipped rather than
	// announced to.
	hints, err := t.OwnerHints(ctx, pr.Repo)
	if err != nil {
		return nil, err
	}
	for _, hint := range hints {
		if isRequired[hint] {
			add(hint, ChannelFromHint)
		}
	}

	sort.Strings(required)
	for _, owner := range required {
		add(owner, ChannelFromRequired)
	}
	return ordered, nil
}

// waitingForOwners reads the authored answer off the open actions about this
// pull request.
//
// Open ones only: a closed wait names whoever it was for at the time, which is
// history rather than where to announce now.
func (t *Tx) waitingForOwners(ctx context.Context, prID string) ([]string, error) {
	rows, err := t.tx.QueryContext(ctx, `
		SELECT a.waiting_for FROM action a
		JOIN action_pr link ON link.action_id = a.id AND link.role = ?
		WHERE link.pr_id = ? AND a.closed_at IS NULL AND a.waiting_for IS NOT NULL
		ORDER BY a.n`, RoleSubject, prID)
	if err != nil {
		return nil, fmt.Errorf("reading who %s is waiting for: %w", prID, err)
	}
	defer rows.Close()

	var owners []string
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			return nil, fmt.Errorf("reading who %s is waiting for: %w", prID, err)
		}
		owners = append(owners, owner)
	}
	return owners, rows.Err()
}

// decodeOwners reads the observed required_owners list.
func decodeOwners(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var owners []string
	if err := json.Unmarshal([]byte(raw), &owners); err != nil {
		return nil, fmt.Errorf("required_owners is not a list: %w", err)
	}
	return owners, nil
}
