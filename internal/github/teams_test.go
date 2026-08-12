package github

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestGroupTeamsByOrg pins the half that is written: teams belong to
// organisations, so a query plan is one entry per org with the slugs wanted
// from it.
func TestGroupTeamsByOrg(t *testing.T) {
	got, err := groupTeamsByOrg([]string{
		"@acme/platform", "acme/storage", "@other/api",
		"acme/platform", // the same team named twice is asked about once
	})
	if err != nil {
		t.Fatalf("groupTeamsByOrg() returned error: %v", err)
	}
	want := map[string][]string{
		"acme":  {"platform", "storage"},
		"other": {"api"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("groupTeamsByOrg() = %v, want %v", got, want)
	}
}

func TestGroupTeamsRejectsAUser(t *testing.T) {
	for _, bad := range []string{"@alice", "alice", "acme/", "/platform", ""} {
		if _, err := groupTeamsByOrg([]string{bad}); err == nil {
			t.Errorf("groupTeamsByOrg(%q) returned nil, want an error", bad)
		}
	}
}

// TestTeamMembersWithNothingToLookUp: asking about no teams is not an error,
// so a CODEOWNERS naming only users does not need special-casing by callers.
func TestTeamMembersWithNothingToLookUp(t *testing.T) {
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		t.Fatal("an empty lookup reached the network")
		return nil, nil
	})

	got, err := client.TeamMembers(context.Background(), nil)
	if err != nil {
		t.Fatalf("TeamMembers(nil) returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("TeamMembers(nil) = %v, want an empty table", got)
	}
}

// TestTeamMembersValidatesBeforeReachingTheNetwork: a malformed reference is
// the caller's bug and should not cost a round trip to discover.
func TestTeamMembersValidatesBeforeReachingTheNetwork(t *testing.T) {
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		t.Fatal("a malformed team reached the network")
		return nil, nil
	})

	_, err := client.TeamMembers(context.Background(), []string{"@alice"})
	if err == nil {
		t.Fatal("TeamMembers() accepted a user as a team")
	}
}

// respond builds a Runner returning canned bodies in order, and records the
// queries it was asked. One body per round of paging.
func respond(t *testing.T, bodies ...string) (Runner, *[]string) {
	t.Helper()
	var asked []string
	round := 0
	return func(_ context.Context, query string) ([]byte, error) {
		asked = append(asked, query)
		if round >= len(bodies) {
			t.Fatalf("round %d has no canned response", round)
		}
		body := bodies[round]
		round++
		return []byte(body), nil
	}, &asked
}

// TestTeamMembersReadsEveryTeamInOneQuery: the whole point of the shape is one
// fetch up front, so two teams across two orgs must not be two round trips.
func TestTeamMembersReadsEveryTeamInOneQuery(t *testing.T) {
	run, asked := respond(t, `{"data":{
	  "tm0":{"team":{"members":{"pageInfo":{"hasNextPage":false,"endCursor":""},
	          "nodes":[{"login":"alice"},{"login":"bob"}]}}},
	  "tm1":{"team":{"members":{"pageInfo":{"hasNextPage":false,"endCursor":""},
	          "nodes":[{"login":"alice"},{"login":"carol"}]}}},
	  "tm2":{"team":{"members":{"pageInfo":{"hasNextPage":false,"endCursor":""},
	          "nodes":[{"login":"dave"}]}}}}}`)

	got, err := NewWithRunner(run).TeamMembers(context.Background(),
		[]string{"@acme/storage", "acme/platform", "other/api"})
	if err != nil {
		t.Fatalf("TeamMembers() returned error: %v", err)
	}

	want := map[string][]string{
		"acme/platform": {"alice", "bob"},
		"acme/storage":  {"alice", "carol"},
		"other/api":     {"dave"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TeamMembers() = %v, want %v", got, want)
	}
	if len(*asked) != 1 {
		t.Errorf("made %d requests, want 1", len(*asked))
	}
	// Aliases are positional over the sorted plan, so platform is tm0.
	if !strings.Contains((*asked)[0], `team(slug: "platform")`) {
		t.Error("the query does not name the platform team")
	}
	// Child teams satisfy a parent in GitHub's own resolution, so this must not
	// silently narrow to immediate members.
	if !strings.Contains((*asked)[0], "membership: ALL") {
		t.Error("the query does not ask for child-team members")
	}
}

// TestTeamMembersPagesALargeTeam: a team over 100 needs paging, and an
// incomplete list would make a reviewer's approval fail to satisfy their team.
func TestTeamMembersPagesALargeTeam(t *testing.T) {
	run, asked := respond(t,
		`{"data":{"tm0":{"team":{"members":{"pageInfo":{"hasNextPage":true,"endCursor":"CUR1"},
		   "nodes":[{"login":"alice"}]}}}}}`,
		`{"data":{"tm0":{"team":{"members":{"pageInfo":{"hasNextPage":false,"endCursor":""},
		   "nodes":[{"login":"bob"}]}}}}}`)

	got, err := NewWithRunner(run).TeamMembers(context.Background(), []string{"acme/platform"})
	if err != nil {
		t.Fatalf("TeamMembers() returned error: %v", err)
	}
	want := map[string][]string{"acme/platform": {"alice", "bob"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TeamMembers() = %v, want %v", got, want)
	}
	if len(*asked) != 2 {
		t.Fatalf("made %d requests, want 2", len(*asked))
	}
	if !strings.Contains((*asked)[1], `after: "CUR1"`) {
		t.Error("the second round did not carry the cursor")
	}
}

// TestTeamMembersStopsAskingForAFinishedTeam: one team paging on must not drag
// a finished one through another round, which would duplicate its members.
func TestTeamMembersStopsAskingForAFinishedTeam(t *testing.T) {
	run, asked := respond(t,
		`{"data":{
		  "tm0":{"team":{"members":{"pageInfo":{"hasNextPage":true,"endCursor":"CUR1"},
		    "nodes":[{"login":"alice"}]}}},
		  "tm1":{"team":{"members":{"pageInfo":{"hasNextPage":false,"endCursor":""},
		    "nodes":[{"login":"zoe"}]}}}}}`,
		`{"data":{"tm0":{"team":{"members":{"pageInfo":{"hasNextPage":false,"endCursor":""},
		   "nodes":[{"login":"bob"}]}}}}}`)

	got, err := NewWithRunner(run).TeamMembers(context.Background(),
		[]string{"acme/platform", "acme/storage"})
	if err != nil {
		t.Fatalf("TeamMembers() returned error: %v", err)
	}
	want := map[string][]string{
		"acme/platform": {"alice", "bob"},
		"acme/storage":  {"zoe"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TeamMembers() = %v, want %v", got, want)
	}
	if strings.Contains((*asked)[1], `slug: "storage"`) {
		t.Error("the second round asked about a team that had finished")
	}
}

// TestTeamMembersReportsAMissingOrg: a null organisation is what a token
// without read:org looks like, so the error has to say that rather than leave
// it reading as a missing org.
func TestTeamMembersReportsAMissingOrg(t *testing.T) {
	run, _ := respond(t, `{"data":{"tm0":null},
	  "errors":[{"message":"Could not resolve to an Organization with the login of 'acme'."}]}`)

	_, err := NewWithRunner(run).TeamMembers(context.Background(), []string{"acme/platform"})
	if err == nil {
		t.Fatal("TeamMembers() returned nil for a null organisation")
	}
	if !strings.Contains(err.Error(), "read:org") {
		t.Errorf("error does not mention the scope: %v", err)
	}
	if !strings.Contains(err.Error(), "acme/platform") {
		t.Errorf("error does not name the team: %v", err)
	}
}

// TestTeamMembersReportsAMissingTeam: a team that does not resolve is an error
// and not an empty team, or an approval silently stops satisfying it.
func TestTeamMembersReportsAMissingTeam(t *testing.T) {
	run, _ := respond(t, `{"data":{
	  "tm0":{"team":null},
	  "tm1":{"team":{"members":{"pageInfo":{"hasNextPage":false,"endCursor":""},
	    "nodes":[{"login":"alice"}]}}}}}`)

	_, err := NewWithRunner(run).TeamMembers(context.Background(),
		[]string{"acme/platform", "acme/storage"})
	if err == nil {
		t.Fatal("TeamMembers() treated an unresolved team as empty")
	}
	if !strings.Contains(err.Error(), "acme/platform") {
		t.Errorf("error does not name the team: %v", err)
	}
}

// TestTeamMembersRecordsAnEmptyTeam: an empty team is a real answer, and the
// key has to be present or a caller cannot tell it from "not looked up".
func TestTeamMembersRecordsAnEmptyTeam(t *testing.T) {
	run, _ := respond(t, `{"data":{"tm0":{"team":{"members":{
	  "pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}`)

	got, err := NewWithRunner(run).TeamMembers(context.Background(), []string{"acme/platform"})
	if err != nil {
		t.Fatalf("TeamMembers() returned error: %v", err)
	}
	members, ok := got["acme/platform"]
	if !ok {
		t.Fatal("an empty team is absent from the table, not empty in it")
	}
	if len(members) != 0 {
		t.Errorf("members = %v, want empty", members)
	}
}

// TestTeamMembersPropagatesRateLimiting: rate limiting means wait, not that
// anything was wrong with the question.
func TestTeamMembersPropagatesRateLimiting(t *testing.T) {
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		return nil, ErrRateLimited
	})

	_, err := client.TeamMembers(context.Background(), []string{"acme/platform"})
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("TeamMembers() error = %v, want ErrRateLimited", err)
	}
}

// TestTeamMembersDecodesDespiteANonZeroExit: gh exits non-zero whenever an
// alias fails to resolve, and the rest of that response is good.
func TestTeamMembersDecodesDespiteANonZeroExit(t *testing.T) {
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		return []byte(`{"data":{"tm0":{"team":{"members":{
		  "pageInfo":{"hasNextPage":false,"endCursor":""},
		  "nodes":[{"login":"alice"}]}}}}}`), errors.New("exit status 1")
	})

	got, err := client.TeamMembers(context.Background(), []string{"acme/platform"})
	if err != nil {
		t.Fatalf("TeamMembers() returned error: %v", err)
	}
	if want := map[string][]string{"acme/platform": {"alice"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("TeamMembers() = %v, want %v", got, want)
	}
}
