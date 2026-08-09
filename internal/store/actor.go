package store

import "strings"

// Actor identifies who is making a change.
//
// It is not just a label for the log: it decides which columns may be
// written. Sync actors may write only observed fields, and everyone else only
// authored ones. That is the sketch's founding rule — sync cannot overwrite a
// judgement, and a judgement cannot invent a fact — and it is enforced in
// Tx.Update rather than trusted to callers.
type Actor string

const (
	ActorHuman       Actor = "human"
	ActorAgentClaude Actor = "agent:claude"
	ActorSyncGitHub  Actor = "sync:github"
	ActorSyncJira    Actor = "sync:jira"
	ActorSyncSlack   Actor = "sync:slack"

	// ActorSlackManual is a person entering by hand what Slack sync would
	// have observed.
	//
	// It is a sync actor because what it writes are observed columns, and it
	// is a distinct one because the log should not claim an integration
	// reported something a person typed. The sketch's own cautionary tale is
	// exactly this: a note claiming a broken integration had recovered,
	// recorded as state rather than as an unverified report.
	ActorSlackManual Actor = "sync:slack-manual"
)

// syncPrefix marks an actor as a sync. New sync sources are named
// "sync:<source>" and need no code change here.
const syncPrefix = "sync:"

// writes returns the one kind of field this actor is allowed to change.
func (a Actor) writes() FieldKind {
	if strings.HasPrefix(string(a), syncPrefix) {
		return Observed
	}
	return Authored
}
