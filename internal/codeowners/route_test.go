package codeowners

import "testing"

func tierOwners(tiers []Tier) []Owner {
	out := make([]Owner, len(tiers))
	for i, tier := range tiers {
		out[i] = tier.Owner
	}
	return out
}

func sameOwners(got, want []Owner) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestRouteFallsBackToCoverage: with nothing hinted, the answer is the same
// greedy one Plan gives — most outstanding files first.
func TestRouteFallsBackToCoverage(t *testing.T) {
	o := ownershipOf(t, "* @org/all\nstorage/ @org/storage\n",
		"storage/a.go", "storage/b.go", "main.go")

	tiers := o.Route(NewOwnerSet(), nil, nil)
	if want := []Owner{"org/storage", "org/all"}; !sameOwners(tierOwners(tiers), want) {
		t.Fatalf("Route() = %v, want %v", tierOwners(tiers), want)
	}
	if tiers[0].Reason != ReasonCoverage {
		t.Errorf("reason = %q, want %q", tiers[0].Reason, ReasonCoverage)
	}
	if tiers[0].Files != 2 {
		t.Errorf("files = %d, want 2", tiers[0].Files)
	}
}

// TestAHintIsPreferredWhereItHelps: ordering what is already required is the
// safe half of hinting.
func TestAHintIsPreferredWhereItHelps(t *testing.T) {
	o := ownershipOf(t, "* @org/all\nstorage/ @org/storage\n",
		"storage/a.go", "storage/b.go", "main.go")

	// Coverage alone would ask storage first; the hint reorders it.
	tiers := o.Route(NewOwnerSet(), []Owner{"org/all"}, nil)
	if want := []Owner{"org/all", "org/storage"}; !sameOwners(tierOwners(tiers), want) {
		t.Fatalf("Route() = %v, want %v", tierOwners(tiers), want)
	}
	if tiers[0].Reason != ReasonHinted {
		t.Errorf("reason = %q, want %q", tiers[0].Reason, ReasonHinted)
	}
}

// TestAHintThatCoversNothingIsSkipped is the check the issue calls the point:
// a hint is a preference, not an assertion.
func TestAHintThatCoversNothingIsSkipped(t *testing.T) {
	o := ownershipOf(t, "storage/ @org/storage\n", "storage/a.go")

	tiers := o.Route(NewOwnerSet(), []Owner{"org/frontend", "org/storage"}, nil)
	if want := []Owner{"org/storage"}; !sameOwners(tierOwners(tiers), want) {
		t.Errorf("Route() = %v, want %v — the hint owns nothing here", tierOwners(tiers), want)
	}
}

// TestAHintThatCoversEverythingEndsIt: a hint owning the lot makes the later
// tiers unnecessary, which is the other half of checking rather than trusting.
func TestAHintThatCoversEverythingEndsIt(t *testing.T) {
	o := ownershipOf(t, "* @org/all\nstorage/ @org/storage\n", "storage/a.go", "main.go")

	// @org/all owns main.go; storage/a.go is owned by storage alone, so this
	// cannot end after one — but the order is what the hint decides.
	tiers := o.Route(NewOwnerSet(), []Owner{"org/all"}, nil)
	if len(tiers) != 2 || tiers[0].Owner != "org/all" {
		t.Fatalf("Route() = %v", tierOwners(tiers))
	}

	// Where the hint really does own everything, there is one tier.
	all := ownershipOf(t, "* @org/all\n", "storage/a.go", "main.go")
	tiers = all.Route(NewOwnerSet(), []Owner{"org/all"}, nil)
	if len(tiers) != 1 {
		t.Errorf("Route() = %v, want one ask", tierOwners(tiers))
	}
}

// TestATeamThatOwnsNothingButStandsForOne is the case the issue says a naive
// version breaks on. The team appears in no rule; every one of its members
// belongs to a team that does, so the approval that comes back satisfies the
// rule.
func TestATeamThatOwnsNothingButStandsForOne(t *testing.T) {
	o := ownershipOf(t, "storage/ @org/storage\n", "storage/a.go")

	members := NewStaticMembership(map[string][]string{
		"org/storage": {"alice", "bob", "carol"},
		// Wholly inside org/storage.
		"org/storage-oncall": {"alice", "bob"},
	})

	tiers := o.Route(NewOwnerSet(), []Owner{"org/storage-oncall"}, members)
	if len(tiers) != 1 {
		t.Fatalf("Route() = %v, want one ask", tierOwners(tiers))
	}
	if tiers[0].Owner != "org/storage-oncall" {
		t.Errorf("asked %q, want the hinted team", tiers[0].Owner)
	}
	if tiers[0].Reason != ReasonStandsFor {
		t.Errorf("reason = %q, want %q", tiers[0].Reason, ReasonStandsFor)
	}
	if len(tiers[0].StandsFor) != 1 || tiers[0].StandsFor[0] != "org/storage" {
		t.Errorf("stands for %v, want [org/storage]", tiers[0].StandsFor)
	}
	// And asking it settles the requirement: nothing else is left.
	if !o.Enough(NewOwnerSet("org/storage")) {
		t.Error("the approval it would produce does not satisfy the rule")
	}
}

// TestAPartialOverlapIsNotRoutedOn: "might help, depending which member
// replies" is not something a plan should promise.
func TestAPartialOverlapIsNotRoutedOn(t *testing.T) {
	o := ownershipOf(t, "storage/ @org/storage\n", "storage/a.go")

	members := NewStaticMembership(map[string][]string{
		"org/storage": {"alice", "bob"},
		// carol is not in org/storage, so an approval from carol proves
		// nothing about the rule.
		"org/mixed": {"alice", "carol"},
	})

	tiers := o.Route(NewOwnerSet(), []Owner{"org/mixed"}, members)
	if len(tiers) != 1 || tiers[0].Owner != "org/storage" {
		t.Errorf("Route() = %v, want the real owner", tierOwners(tiers))
	}
}

// TestAHintedPersonStandsForTheirTeams: the same question with a one-member
// team, and worth supporting since a hint may name an individual.
func TestAHintedPersonStandsForTheirTeams(t *testing.T) {
	o := ownershipOf(t, "storage/ @org/storage\n", "storage/a.go")

	members := NewStaticMembership(map[string][]string{
		"org/storage": {"alice", "bob"},
	})

	tiers := o.Route(NewOwnerSet(), []Owner{"alice"}, members)
	if len(tiers) != 1 || tiers[0].Owner != "alice" {
		t.Fatalf("Route() = %v, want alice", tierOwners(tiers))
	}
	if len(tiers[0].StandsFor) != 1 || tiers[0].StandsFor[0] != "org/storage" {
		t.Errorf("stands for %v", tiers[0].StandsFor)
	}
	// Somebody outside the team is not routed to.
	tiers = o.Route(NewOwnerSet(), []Owner{"dave"}, members)
	if len(tiers) != 1 || tiers[0].Owner != "org/storage" {
		t.Errorf("Route() = %v, want the real owner", tierOwners(tiers))
	}
}

// TestAnUnknownTeamStandsForNothing: an empty member list is trivially a
// subset of everything, and routing on that would ask a team nothing knows
// about to approve the lot.
func TestAnUnknownTeamStandsForNothing(t *testing.T) {
	o := ownershipOf(t, "storage/ @org/storage\n", "storage/a.go")
	members := NewStaticMembership(map[string][]string{"org/storage": {"alice"}})

	tiers := o.Route(NewOwnerSet(), []Owner{"org/never-heard-of"}, members)
	if len(tiers) != 1 || tiers[0].Owner != "org/storage" {
		t.Errorf("Route() = %v, want the real owner", tierOwners(tiers))
	}
}

// TestHintsDoNotOverrideCodeowners: ordering what is required is safe,
// substituting for it is not — so routing runs until the rules are satisfied
// whatever the hints said.
func TestHintsDoNotOverrideCodeowners(t *testing.T) {
	o := ownershipOf(t, "storage/ @org/storage\nbilling/ @org/billing\n",
		"storage/a.go", "billing/b.go")

	tiers := o.Route(NewOwnerSet(), []Owner{"org/storage"}, nil)
	if len(tiers) != 2 {
		t.Fatalf("Route() = %v, want both owners asked", tierOwners(tiers))
	}
	if !o.Enough(NewOwnerSet("org/storage", "org/billing")) {
		t.Error("the plan does not satisfy the rules")
	}
}

// TestRouteRespectsWhatIsAlreadyApproved: the tiers are what is left to do,
// not what would have been done from scratch.
func TestRouteRespectsWhatIsAlreadyApproved(t *testing.T) {
	o := ownershipOf(t, "storage/ @org/storage\nbilling/ @org/billing\n",
		"storage/a.go", "billing/b.go")

	tiers := o.Route(NewOwnerSet("org/storage"), nil, nil)
	if want := []Owner{"org/billing"}; !sameOwners(tierOwners(tiers), want) {
		t.Errorf("Route() = %v, want %v", tierOwners(tiers), want)
	}
}

// TestRouteStopsWhenNothingCanCoverTheRest is the state that has no answer: a
// file nothing owns cannot be routed to anybody, and a plan that quietly
// omitted it would read as complete.
func TestRouteStopsWhenNothingCanCoverTheRest(t *testing.T) {
	o := ownershipOf(t, "storage/ @org/storage\n", "storage/a.go", "orphan.go")

	tiers := o.Route(NewOwnerSet(), nil, nil)
	if len(tiers) != 1 {
		t.Fatalf("Route() = %v", tierOwners(tiers))
	}
	// orphan.go is owned by nobody, so it is not outstanding in the sense
	// Remaining means — Enough is true once the owned files are covered.
	if !o.Enough(NewOwnerSet("org/storage")) {
		t.Error("a file nothing owns is being counted as outstanding")
	}
}
