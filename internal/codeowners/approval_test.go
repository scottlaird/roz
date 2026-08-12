package codeowners

import (
	"reflect"
	"testing"
)

// teams is one person in two owning teams, one in a single team, and one in
// none — which is the shape that makes the expansion matter.
func teams() StaticTeams {
	return NewStaticTeams(map[string][]string{
		"@org/platform": {"alice", "bob"},
		"@org/storage":  {"alice", "carol"},
		"@org/api":      {"dave"},
	})
}

// TestApprovalExpandsAUserIntoTheirTeams is the mismatch this exists for.
// CODEOWNERS names teams; a review comes back naming a person. Comparing the
// two directly answers "not approved" however many of the right people have.
func TestApprovalExpandsAUserIntoTheirTeams(t *testing.T) {
	approved := Approval([]string{"bob"}, teams())

	if !approved.Contains("org/platform") {
		t.Error("bob's approval did not satisfy the team he is in")
	}
	if !approved.Contains("bob") {
		t.Error("bob's approval did not satisfy bob, who may be a named owner")
	}
	if approved.Contains("org/storage") {
		t.Error("bob's approval satisfied a team he is not in")
	}
}

// TestOneReviewerSatisfiesEveryTeamTheyAreIn: alice is in two owning teams, so
// her single approval covers files owned by either.
func TestOneReviewerSatisfiesEveryTeamTheyAreIn(t *testing.T) {
	o := ownershipOf(t, overlapping, "README.md", "storage/engine.go")

	// Two files, two different owning teams, and no sole approver among the
	// teams themselves.
	if got := o.SoleApprovers(); len(got) != 0 {
		t.Fatalf("SoleApprovers() = %v, want nobody at team granularity", got)
	}

	// But alice is in both, so one review from her finishes it.
	if !o.Enough(Approval([]string{"alice"}, teams())) {
		t.Errorf("alice's approval left %v outstanding", o.Remaining(Approval([]string{"alice"}, teams())))
	}
	// Bob is in only one of them, so his does not.
	if o.Enough(Approval([]string{"bob"}, teams())) {
		t.Error("bob's approval was treated as covering a team he is not in")
	}
}

// TestApprovalOfAnUnknownUser: someone outside every team approves only as
// themselves. Not an error — they may be a directly named owner, and a
// membership table that has not heard of them should not fail the question.
func TestApprovalOfAnUnknownUser(t *testing.T) {
	approved := Approval([]string{"@Erin"}, teams())

	if got, want := approved.Sorted(), []Owner{"erin"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Approval(erin) = %v, want %v", got, want)
	}
}

// TestOwnerReferencesAreNormalised: the same team written three ways is one
// owner. Comparing raw strings would make them three, and a change would look
// unapproved because the file said @Org/Platform and the review said platform.
func TestOwnerReferencesAreNormalised(t *testing.T) {
	file, err := ParseString("* @Org/Platform\n")
	if err != nil {
		t.Fatalf("ParseString() returned error: %v", err)
	}
	o := file.Of([]string{"main.go"})

	for _, written := range []string{"@org/platform", "org/platform", "@ORG/Platform"} {
		if !o.Enough(NewOwnerSet(written)) {
			t.Errorf("%q did not match the owner as written in the file", written)
		}
	}
}

func TestNoTeamsResolvesOnlyTheUser(t *testing.T) {
	approved := Approval([]string{"alice"}, NoTeams{})

	if got, want := approved.Sorted(), []Owner{"alice"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Approval with NoTeams = %v, want %v", got, want)
	}
}

// TestUsefulAfterATeamApproval is the working case end to end: a review from
// one person covers the files their team owns, and the answer to "who now" is
// the short list rather than everyone the file mentions.
func TestUsefulAfterATeamApproval(t *testing.T) {
	o := ownershipOf(t, overlapping,
		"README.md", "cmd/main.go", // platform
		"storage/engine.go",    // storage
		"storage/schema.proto", // api
	)

	// Bob is platform only.
	useful := o.Useful(Approval([]string{"bob"}, teams()))
	got := make([]Owner, len(useful))
	for i, c := range useful {
		got[i] = c.Owner
	}
	if want := []Owner{"org/api", "org/storage"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Useful() after bob = %v, want %v", got, want)
	}

	// Alice is platform and storage, so only the api team is left.
	useful = o.Useful(Approval([]string{"alice"}, teams()))
	if len(useful) != 1 || useful[0].Owner != "org/api" {
		t.Errorf("Useful() after alice = %v, want just the api team", useful)
	}
}

func TestNewStaticTeamsInvertsTheTable(t *testing.T) {
	got := teams().TeamsOf("alice")

	if want := []Owner{"org/platform", "org/storage"}; !reflect.DeepEqual(got, want) {
		t.Errorf("TeamsOf(alice) = %v, want %v", got, want)
	}
	if got := teams().TeamsOf("nobody"); got != nil {
		t.Errorf("TeamsOf(nobody) = %v, want nothing", got)
	}
}
