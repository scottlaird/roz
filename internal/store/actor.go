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

	// ActorJiraManual is the same arrangement for Jira: a person entering
	// what Jira sync would have observed, kept distinct so the log does not
	// claim Jira said it.
	ActorJiraManual Actor = "sync:jira-manual"

	// ActorGitHubIssueManual is the same again for GitHub issues. Distinct
	// from ActorSyncGitHub, which reads pull requests for real.
	ActorGitHubIssueManual Actor = "sync:github-issue-manual"
)

// ManualActorFor is the actor to record when a person hand-enters what a
// tracker's sync would have observed. One per tracker, so the log never says
// "jira" about something read from GitHub.
func ManualActorFor(tracker string) (Actor, error) {
	switch tracker {
	case TrackerJira:
		return ActorJiraManual, nil
	case TrackerGitHub:
		return ActorGitHubIssueManual, nil
	default:
		return "", ValidateTracker(tracker)
	}
}

const (

	// ActorVerify records that someone checked an item against reality.
	//
	// A sync actor, because last_verified_at is observed: verifying is asking
	// the world whether the record is still true, not deciding what it should
	// say. The sketch groups `roz verify` with `roz sync` for that reason —
	// they are the only writers of observed fields.
	//
	// Distinct from the sync sources for the same reason ActorSlackManual is:
	// nothing reported this, a person went and looked, and the log should say
	// which.
	ActorVerify Actor = "sync:verify"

	// ActorPredicate closes actions whose predicate has come true.
	//
	// It is deliberately not a sync actor. Closing writes authored columns,
	// and it should: a predicate verb is a rule a person wrote into the
	// vocabulary, so closing on it is that judgement being carried out, not
	// an observation. Sync reports what GitHub says; this decides what that
	// means, and the log should not confuse the two.
	ActorPredicate Actor = "predicate"
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
