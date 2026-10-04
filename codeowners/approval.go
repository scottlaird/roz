package codeowners

import "sort"

// OwnerSet is a set of owners, normalised.
type OwnerSet map[Owner]bool

// NewOwnerSet builds a set from owner references, normalising each.
func NewOwnerSet(refs ...string) OwnerSet {
	set := OwnerSet{}
	for _, ref := range refs {
		if owner := NormalizeOwner(ref); owner != "" {
			set[owner] = true
		}
	}
	return set
}

func (s OwnerSet) Add(owners ...Owner) {
	for _, owner := range owners {
		s[owner] = true
	}
}

func (s OwnerSet) Contains(owner Owner) bool { return s[owner] }

// ContainsAny reports whether the set holds at least one of owners, which is
// the test CODEOWNERS actually applies: a file needs an approval from one of
// its owners, not from all of them.
func (s OwnerSet) ContainsAny(owners []Owner) bool {
	for _, owner := range owners {
		if s[owner] {
			return true
		}
	}
	return false
}

// Intersect returns the members also present in owners.
func (s OwnerSet) Intersect(owners []Owner) OwnerSet {
	keep := OwnerSet{}
	for _, owner := range owners {
		if s[owner] {
			keep[owner] = true
		}
	}
	return keep
}

func (s OwnerSet) Clone() OwnerSet {
	clone := make(OwnerSet, len(s))
	for owner := range s {
		clone[owner] = true
	}
	return clone
}

// Sorted returns the members in a stable order.
func (s OwnerSet) Sorted() []Owner {
	out := make([]Owner, 0, len(s))
	for owner := range s {
		out = append(out, owner)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Teams resolves which teams a user belongs to.
//
// This exists because of a mismatch that is easy to miss: CODEOWNERS names
// teams, and a review comes back naming a person. Asking "has @org/platform
// approved" against a list of logins answers no every time, however many
// members of that team have approved.
type Teams interface {
	// TeamsOf returns the teams a user belongs to, as owner references. An
	// unknown user is not an error: they simply approve only as themselves.
	TeamsOf(user Owner) []Owner
}

// StaticTeams is a fixed membership table — for tests, and for a snapshot
// fetched once and reused across several questions about the same change.
type StaticTeams map[Owner][]Owner

// NewStaticTeams builds a membership table from team to members, which is the
// direction the GitHub API answers in, and inverts it to the direction the
// question needs.
func NewStaticTeams(members map[string][]string) StaticTeams {
	teams := StaticTeams{}
	for team, logins := range members {
		owner := NormalizeOwner(team)
		for _, login := range logins {
			user := NormalizeOwner(login)
			teams[user] = append(teams[user], owner)
		}
	}
	for user := range teams {
		sort.Slice(teams[user], func(i, j int) bool { return teams[user][i] < teams[user][j] })
	}
	return teams
}

func (t StaticTeams) TeamsOf(user Owner) []Owner { return t[user] }

// NoTeams resolves nothing. Every user approves only as themselves, which is
// the right answer for a CODEOWNERS file naming no teams and the wrong one for
// most real ones — so it is a named type rather than a nil default, to make
// choosing it deliberate.
type NoTeams struct{}

func (NoTeams) TeamsOf(Owner) []Owner { return nil }

// Approval turns the people who approved into the owners their approvals
// satisfy: themselves, and every team they belong to.
//
// The expansion is the point. A review arrives as a login, and the file it has
// to cover is owned by a team; without mapping the one to the other, a change
// fully approved by the right people still reads as outstanding. And a person
// in two owning teams satisfies both at once, which is why this returns a set
// rather than a count.
func Approval(reviewers []string, teams Teams) OwnerSet {
	approved := OwnerSet{}
	for _, raw := range reviewers {
		user := NormalizeOwner(raw)
		if user == "" {
			continue
		}
		approved.Add(user)
		approved.Add(teams.TeamsOf(user)...)
	}
	return approved
}
