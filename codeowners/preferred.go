package codeowners

import "sort"

// Preferred picks the one owner a change is waiting on, given an ordered list
// of owners to prefer. It is for saying "this is with that team now", where
// Route answers the different question of everyone to ask, in order.
//
// The first preferred owner that qualifies wins. To qualify it must own at
// least one outstanding file outright, and be able to settle every
// outstanding file: each one is either its own, or owned by a team every one
// of its members belongs to. The first condition keeps a team nested inside a
// broad one from claiming changes that only touch the broad team's files;
// the second keeps it from being named for a change it can't finish.
//
// When no preferred owner qualifies, the answer is the owner listed first on
// the most outstanding files: CODEOWNERS authors put the team that knows the
// code first. Ties go to the name that sorts first.
//
// ok is false when nothing is outstanding.
func (o *Ownership) Preferred(approved OwnerSet, preferred []Owner, members Membership) (Owner, bool) {
	if members == nil {
		members = NoMembership{}
	}
	remaining := o.Remaining(approved)
	if len(remaining) == 0 {
		return "", false
	}
	for _, candidate := range preferred {
		if o.settlesAll(candidate, remaining, members) {
			return candidate, true
		}
	}
	return firstListedOn(o, remaining), true
}

// settlesAll reports whether candidate owns at least one of paths outright and
// can settle every one of them, directly or by standing for an owner.
func (o *Ownership) settlesAll(candidate Owner, paths []string, members Membership) bool {
	direct := false
	for _, path := range paths {
		owners := o.OwnersOf(path)
		if containsOwner(owners, candidate) {
			direct = true
			continue
		}
		if len(standsFor(candidate, owners, members)) == 0 {
			return false
		}
	}
	return direct
}

// firstListedOn is the owner listed first on the most of paths, ties to the
// name that sorts first.
func firstListedOn(o *Ownership, paths []string) Owner {
	counts := map[Owner]int{}
	for _, path := range paths {
		counts[o.OwnersOf(path)[0]]++
	}
	owners := make([]Owner, 0, len(counts))
	for owner := range counts {
		owners = append(owners, owner)
	}
	sort.Slice(owners, func(i, j int) bool {
		if counts[owners[i]] != counts[owners[j]] {
			return counts[owners[i]] > counts[owners[j]]
		}
		return owners[i] < owners[j]
	})
	return owners[0]
}

func containsOwner(owners []Owner, want Owner) bool {
	for _, o := range owners {
		if o == want {
			return true
		}
	}
	return false
}
