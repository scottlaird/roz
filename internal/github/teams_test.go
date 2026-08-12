package github

import (
	"context"
	"errors"
	"reflect"
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

// TestTeamMembersIsNotImplementedYet: the placeholder fails loudly rather than
// returning an empty table. An empty one is a legitimate answer, so a silent
// one would make every question about teams quietly answer "still needed".
func TestTeamMembersIsNotImplementedYet(t *testing.T) {
	client := NewWithRunner(func(context.Context, string) ([]byte, error) {
		t.Fatal("the stub reached the network")
		return nil, nil
	})

	_, err := client.TeamMembers(context.Background(), []string{"acme/platform"})
	if !errors.Is(err, ErrNotImplemented) {
		t.Errorf("TeamMembers() error = %v, want ErrNotImplemented", err)
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
	if errors.Is(err, ErrNotImplemented) {
		t.Error("the input was not validated before the unimplemented part")
	}
}
