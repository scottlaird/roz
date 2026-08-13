package codeowners

import "sort"

// Membership answers who belongs to a team.
//
// The opposite direction from Teams, and needed for a different job. Reducing
// what is left after an approval asks "which teams does this approver stand
// for", which is per-person; routing asks "would asking this team produce an
// approval that counts", which is per-team and has to be answered before
// anybody has approved.
type Membership interface {
	MembersOf(team Owner) []Owner
}

// StaticMembership is a Membership from a map of team to logins, as
// github.Client.TeamMembers returns.
type StaticMembership map[Owner][]Owner

// NewStaticMembership normalises a team-to-logins map.
func NewStaticMembership(members map[string][]string) StaticMembership {
	m := StaticMembership{}
	for team, logins := range members {
		owner := NormalizeOwner(team)
		for _, login := range logins {
			m[owner] = append(m[owner], NormalizeOwner(login))
		}
		sort.Slice(m[owner], func(i, j int) bool { return m[owner][i] < m[owner][j] })
	}
	return m
}

func (m StaticMembership) MembersOf(team Owner) []Owner { return m[team] }

// NoMembership knows nothing, which is what a caller that cannot reach GitHub
// has. Routing still works from CODEOWNERS alone; only the subset case is lost.
type NoMembership struct{}

func (NoMembership) MembersOf(Owner) []Owner { return nil }

// Reason says why a tier was chosen.
const (
	// ReasonCoverage: this owner covers outstanding files directly.
	ReasonCoverage = "covers outstanding files"
	// ReasonHinted: the repository prefers this owner, and it covers
	// outstanding files.
	ReasonHinted = "preferred, and covers outstanding files"
	// ReasonStandsFor: the repository prefers this owner, which owns nothing
	// itself — but every one of its members belongs to an owner that is
	// outstanding, so asking it produces an approval that counts.
	ReasonStandsFor = "preferred, and its members all belong to an owner"
)

// Tier is one ask, and why.
type Tier struct {
	// Owner is who to ask.
	Owner Owner
	// Files is how many outstanding files this ask settles.
	Files int
	// Reason is why this one, from the constants above.
	Reason string
	// StandsFor are the owners an approval from this one would satisfy, where
	// it is not itself an owner. Empty when it owns the files directly.
	StandsFor []Owner
}

// Route chooses who to ask, in order, until every owned file is covered.
//
// Hints are a preference, not an assertion. A repository lists the owners it
// would rather go to first, and each is checked against what is actually
// outstanding before it is used: a hinted owner that owns nothing in this
// particular change is skipped rather than asked, and one that owns everything
// makes the later tiers unnecessary. That check is the whole point — it is
// what stops a hint becoming a habit nobody revisits.
//
// A hint can also be a team that appears in no rule at all, when its members
// all belong to a team that does. Asked by name it owns nothing; asked as
// "would an approval from a member of this satisfy an outstanding owner", it
// does. That is a question about membership rather than names, and it is the
// case that a naive implementation gets wrong.
//
// Hints never override CODEOWNERS. Ordering what is already required is safe;
// substituting for a required owner is not, so routing continues until the
// rules are satisfied whatever the hints said.
//
// Where nothing is hinted, or no hint helps, the choice is the owner covering
// the most outstanding files — greedy rather than exact, since minimum set
// cover is NP-hard and being one ask off optimal costs a review request.
// First-listed breaks a tie, which is safe because a rule with several owners
// needs only one of them.
func (o *Ownership) Route(approved OwnerSet, hints []Owner, members Membership) []Tier {
	if members == nil {
		members = NoMembership{}
	}
	have := approved.Clone()

	var tiers []Tier
	for {
		useful := o.Useful(have)
		if len(useful) == 0 {
			return tiers
		}

		tier, ok := nextTier(o, have, useful, hints, members)
		if !ok {
			return tiers
		}
		tiers = append(tiers, tier)

		// What this ask settles: the owner itself where it owns the files, and
		// whatever it stands for where it does not.
		have.Add(tier.Owner)
		have.Add(tier.StandsFor...)
	}
}

// nextTier picks the next ask: the first hint that would help, else the owner
// covering the most.
func nextTier(o *Ownership, have OwnerSet, useful []Coverage, hints []Owner, members Membership) (Tier, bool) {
	outstanding := make([]Owner, 0, len(useful))
	covers := map[Owner]int{}
	for _, c := range useful {
		outstanding = append(outstanding, c.Owner)
		covers[c.Owner] = c.Files
	}

	for _, hint := range hints {
		if have.Contains(hint) {
			continue
		}
		// The straightforward case: the hint is itself an owner with something
		// outstanding.
		if n, ok := covers[hint]; ok {
			return Tier{Owner: hint, Files: n, Reason: ReasonHinted}, true
		}
		// The case worth having: it owns nothing, but an approval from it
		// would satisfy something that is outstanding.
		if stands := standsFor(hint, outstanding, members); len(stands) > 0 {
			files := 0
			for _, owner := range stands {
				files += covers[owner]
			}
			return Tier{
				Owner: hint, Files: files,
				Reason: ReasonStandsFor, StandsFor: stands,
			}, true
		}
	}

	// Useful is sorted most-files-first, then by name, so the head is both the
	// best ask and a stable choice.
	return Tier{Owner: useful[0].Owner, Files: useful[0].Files, Reason: ReasonCoverage}, true
}

// standsFor returns the outstanding owners an approval from candidate would
// satisfy, because every one of its members belongs to them.
//
// Subset, not overlap. A team wholly inside an owning team is unambiguous:
// whoever answers, the approval counts. A team that overlaps partly means
// "this might help, depending which member replies", and a plan that says
// "ask these" should not contain a maybe — so a partial overlap is not routed
// on. It is not wrong to ask them, it is just not something to promise.
//
// A candidate with no members is either a person or a team nothing knows
// about. A person stands for the owners they belong to, which is the same
// question with a one-member team; a team nothing knows about stands for
// nothing, since an empty set is trivially a subset of everything and routing
// on that would ask an unknown team for everything.
func standsFor(candidate Owner, outstanding []Owner, members Membership) []Owner {
	people := members.MembersOf(candidate)
	if len(people) == 0 {
		if candidate.IsTeam() {
			return nil
		}
		// A person: their own membership is what counts.
		people = []Owner{candidate}
	}

	var stands []Owner
	for _, owner := range outstanding {
		if owner == candidate {
			continue
		}
		within := members.MembersOf(owner)
		if len(within) == 0 {
			continue
		}
		if subset(people, within) {
			stands = append(stands, owner)
		}
	}
	sort.Slice(stands, func(i, j int) bool { return stands[i] < stands[j] })
	return stands
}

// subset reports whether every one of these is one of those.
func subset(these, those []Owner) bool {
	index := make(map[Owner]bool, len(those))
	for _, o := range those {
		index[o] = true
	}
	for _, o := range these {
		if !index[o] {
			return false
		}
	}
	return true
}
