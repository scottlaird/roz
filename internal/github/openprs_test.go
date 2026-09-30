package github

import (
	"encoding/json"
	"testing"
)

func TestOpenPRDecode(t *testing.T) {
	var w wireOpenPR
	err := json.Unmarshal([]byte(`{
		"number": 12, "url": "u", "title": "t", "baseRefName": "main",
		"author": {"login": "carol"},
		"reviewRequests": {"nodes": [
			{"requestedReviewer": {"login": "alice"}},
			{"requestedReviewer": {"combinedSlug": "Org/Storage"}},
			{"requestedReviewer": null}
		]},
		"assignees": {"nodes": [{"login": "bob"}]},
		"latestReviews": {"nodes": [
			{"state": "CHANGES_REQUESTED", "submittedAt": "2026-09-01T00:00:00Z", "author": {"login": "github-actions", "__typename": "Bot"}},
			{"state": "COMMENTED", "submittedAt": "2026-09-02T00:00:00Z", "author": {"login": "dave", "__typename": "User"}},
			{"state": "PENDING", "submittedAt": "2026-09-02T00:00:00Z", "author": {"login": "frank", "__typename": "User"}}
		]},
		"latestOpinionatedReviews": {"nodes": [
			{"state": "CHANGES_REQUESTED", "author": {"login": "dave", "__typename": "User"}},
			{"state": "APPROVED", "author": {"login": "grace", "__typename": "User"}},
			{"state": "CHANGES_REQUESTED", "author": {"login": "ci", "__typename": "Bot"}}
		]},
		"commits": {"nodes": [{"commit": {"committedDate": "2026-09-03T00:00:00Z", "author": {"user": {"login": "carol"}}}}]},
		"comments": {"nodes": [
			{"createdAt": "2026-09-01T12:00:00Z", "author": {"login": "erin", "__typename": "User"}},
			{"createdAt": "2026-09-01T13:00:00Z", "author": {"login": "erin", "__typename": "User"}},
			{"createdAt": "2026-09-01T14:00:00Z", "author": {"login": "ci", "__typename": "Bot"}}
		]},
		"files": {"nodes": [{"path": "a.go"}, {"path": "b.go"}]}
	}`), &w)
	if err != nil {
		t.Fatal(err)
	}
	pr := w.decode("org", "repo")

	if pr.Key != "org/repo#12" {
		t.Errorf("key = %q", pr.Key)
	}
	if len(pr.RequestedUsers) != 1 || pr.RequestedUsers[0] != "alice" {
		t.Errorf("requested users = %v", pr.RequestedUsers)
	}
	if len(pr.RequestedTeams) != 1 || pr.RequestedTeams[0] != "org/storage" {
		t.Errorf("requested teams = %v", pr.RequestedTeams)
	}
	if len(pr.Reviews) != 1 || pr.Reviews[0].Author != "dave" {
		t.Errorf("bot and pending reviews should be dropped, comments kept; reviews = %+v", pr.Reviews)
	}
	if pr.LastActor != "carol" {
		t.Errorf("last actor = %q, want the latest of commit, review and comment", pr.LastActor)
	}
	if len(pr.ChangesRequestedBy) != 1 || pr.ChangesRequestedBy[0] != "dave" {
		t.Errorf("changes requested by = %v, want dave and no bots", pr.ChangesRequestedBy)
	}
	if len(pr.Approvers) != 1 || pr.Approvers[0] != "grace" {
		t.Errorf("approvers = %v, want grace", pr.Approvers)
	}
	if len(pr.Commenters) != 1 || pr.Commenters[0] != "erin" {
		t.Errorf("commenters = %v, want erin once and no bots", pr.Commenters)
	}
	if len(pr.Files) != 2 {
		t.Errorf("files = %v", pr.Files)
	}
}
