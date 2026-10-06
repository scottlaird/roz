package github

import (
	"context"
	"encoding/json"
	"testing"
	"time"
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
		"reviews": {"nodes": [
			{"state": "COMMENTED", "submittedAt": "2026-09-01T06:00:00Z", "author": {"login": "dave", "__typename": "User"}},
			{"state": "COMMENTED", "submittedAt": "2026-09-01T08:00:00Z", "author": {"login": "heidi", "__typename": "User"}}
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
	// heidi was asked to review again after commenting, which takes her out
	// of latestReviews; dave's older review is not a second one.
	if len(pr.Reviews) != 2 || pr.Reviews[0].Author != "dave" || !pr.Reviews[0].At.Equal(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)) || pr.Reviews[1].Author != "heidi" {
		t.Errorf("bot and pending reviews should be dropped, comments and re-requested reviewers kept, one per reviewer; reviews = %+v", pr.Reviews)
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

func TestOpenPRBranchesDecode(t *testing.T) {
	client := NewWithRunner(fixedRunner(`{"data":{"repository":{"pullRequests":{
		"pageInfo":{"hasNextPage":false},
		"nodes":[
			{"number":7,"url":"u7","headRefName":"top","baseRefName":"bottom","isDraft":true,"reviewDecision":"","updatedAt":"2026-10-05T16:00:00Z","headRepository":{"nameWithOwner":"org/repo"}},
			{"number":8,"url":"u8","headRefName":"fork","baseRefName":"main","updatedAt":"2026-10-05T16:00:00Z","headRepository":{"nameWithOwner":"someone/repo"}}
		]}}}}`))
	branches, err := client.OpenPRBranches(context.Background(), "org/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 {
		t.Fatalf("branches = %+v, want the fork's left out", branches)
	}
	b := branches[0]
	if b.Key != "org/repo#7" || b.BaseRef != "bottom" || !b.Draft || !b.UpdatedAt.Equal(time.Date(2026, 10, 5, 16, 0, 0, 0, time.UTC)) {
		t.Errorf("branch = %+v", b)
	}
}

func TestOpenPRIdleSince(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, tc := range []struct {
		name string
		json string
		want string
	}{
		{"creation when nothing else happened", `{}`, "2026-09-01T00:00:00Z"},
		{"pushes and bots don't count", `{
			"commits": {"nodes": [{"commit": {"committedDate": "2026-09-05T00:00:00Z", "author": {"user": {"login": "carol"}}}}]},
			"reviews": {"nodes": [{"state": "COMMENTED", "submittedAt": "2026-09-05T00:00:00Z", "author": {"login": "copilot-pull-request-reviewer", "__typename": "Bot"}}]},
			"comments": {"nodes": [{"createdAt": "2026-09-05T00:00:00Z", "author": {"login": "ci", "__typename": "Bot"}}]}
		}`, "2026-09-01T00:00:00Z"},
		{"leaving draft restarts the wait", `{
			"timelineItems": {"nodes": [{"createdAt": "2026-09-03T00:00:00Z"}]}
		}`, "2026-09-03T00:00:00Z"},
		{"a person's review or comment, whichever is latest", `{
			"timelineItems": {"nodes": [{"createdAt": "2026-09-02T00:00:00Z"}]},
			"reviews": {"nodes": [{"state": "COMMENTED", "submittedAt": "2026-09-03T00:00:00Z", "author": {"login": "dave", "__typename": "User"}}]},
			"comments": {"nodes": [{"createdAt": "2026-09-04T00:00:00Z", "author": {"login": "carol", "__typename": "User"}}]}
		}`, "2026-09-04T00:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w wireOpenPR
			if err := json.Unmarshal([]byte(tc.json), &w); err != nil {
				t.Fatal(err)
			}
			w.CreatedAt = at("2026-09-01T00:00:00Z")
			if got := w.decode("org", "repo").IdleSince; !got.Equal(at(tc.want)) {
				t.Errorf("idle since = %s, want %s", got.Format(time.RFC3339), tc.want)
			}
		})
	}
}
