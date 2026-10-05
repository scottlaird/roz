package codeowners

import "sort"

// Ownership is the file-to-owners map for one change, and the questions worth
// asking of it.
//
// The map is the whole model. Everything below is a reduction of it, and none
// of them can be answered from a flat list of the owners a pull request
// mentions: whether one person can approve the lot, and who is still worth
// asking once somebody has, are both about which owners appear on which files.
type Ownership struct {
	// Files is every path in the change, in the order given.
	Files []string
	// owners is the owner set per path. A path with no owners has an entry
	// with none, so "unowned" and "not in this change" stay distinguishable.
	owners map[string][]Owner
}

// Of maps a set of changed paths onto their owners.
func (f *File) Of(paths []string) *Ownership {
	o := &Ownership{
		Files:  append([]string(nil), paths...),
		owners: make(map[string][]Owner, len(paths)),
	}
	for _, path := range paths {
		o.owners[path] = f.Owners(path)
	}
	return o
}

// OwnersOf returns who owns one path in the change.
func (o *Ownership) OwnersOf(path string) []Owner { return o.owners[path] }

// Unowned lists the paths no rule assigns an owner to. They cannot block a
// merge, so every question below ignores them — but a surprising number of
// them usually means a pattern that does not match what its author thought.
func (o *Ownership) Unowned() []string {
	var out []string
	for _, path := range o.Files {
		if len(o.owners[path]) == 0 {
			out = append(out, path)
		}
	}
	return out
}

// Owners lists every owner appearing anywhere in the change, sorted.
func (o *Ownership) Owners() []Owner {
	seen := OwnerSet{}
	for _, owners := range o.owners {
		seen.Add(owners...)
	}
	return seen.Sorted()
}

// SoleApprovers lists the owners who could each approve the entire change
// alone: every owned file has, among its owners, either that owner or a team
// its members all belong to (see Reach).
//
// This is the first question worth asking and the one a flat owner list cannot
// answer. A change touching two directories may mention five teams and still
// have one person who covers all of it, or mention two and have nobody. With
// membership, a team nested wholly inside another counts for both: asking it
// produces an approval that satisfies either.
//
// A change with no owned files at all returns nothing rather than everybody:
// nobody is required, so nobody is a sole approver, and saying otherwise would
// invent an approval that is not needed.
func (o *Ownership) SoleApprovers(members Membership) []Owner {
	owned := 0
	for _, path := range o.Files {
		if len(o.owners[path]) > 0 {
			owned++
		}
	}
	if owned == 0 {
		return nil
	}
	var sole []Owner
	for _, r := range o.Reach(OwnerSet{}, members) {
		if r.Files == owned {
			sole = append(sole, r.Owner)
		}
	}
	sort.Slice(sole, func(i, j int) bool { return sole[i] < sole[j] })
	return sole
}

// Remaining lists the paths still needing an approval, given who has approved.
//
// A path is covered when any one of its owners is in approved: CODEOWNERS
// requires an approval from the owners of a file, not from all of them.
func (o *Ownership) Remaining(approved OwnerSet) []string {
	var out []string
	for _, path := range o.Files {
		owners := o.owners[path]
		if len(owners) == 0 {
			continue
		}
		if !approved.ContainsAny(owners) {
			out = append(out, path)
		}
	}
	return out
}

// Useful lists the owners whose approval would cover at least one file that is
// still outstanding, with how many each would cover, most first.
//
// This is the question that makes the difference in practice. A change may
// name six teams; once your own team has approved, four of them own only files
// that are already covered, and asking them achieves nothing. Only the ones
// listed here can move the change forward.
func (o *Ownership) Useful(approved OwnerSet) []Coverage {
	counts := map[Owner]int{}
	for _, path := range o.Remaining(approved) {
		for _, owner := range o.owners[path] {
			counts[owner]++
		}
	}

	out := make([]Coverage, 0, len(counts))
	for owner, n := range counts {
		out = append(out, Coverage{Owner: owner, Files: n})
	}
	// Most files first, then by name, so the order is stable and the head of
	// the list is the person most worth asking.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		return out[i].Owner < out[j].Owner
	})
	return out
}

// Coverage is one owner and how many outstanding files their approval covers.
type Coverage struct {
	Owner Owner
	Files int
}

// Enough reports whether approved covers every owned file.
func (o *Ownership) Enough(approved OwnerSet) bool {
	return len(o.Remaining(approved)) == 0
}

// Reach is what asking one owner would settle: the outstanding files it owns
// directly, plus those owned by any owner it stands for.
type Reach struct {
	Owner Owner
	Files int
	// StandsFor are the other owners in the change an approval from this one
	// would also satisfy, because every one of its members belongs to them.
	StandsFor []Owner
}

// Reach lists, for each owner in the change, how many outstanding files asking
// it would settle, most first. Owners that would settle nothing are left out.
//
// It differs from Useful when teams nest. If every member of @org/storage is
// also in @org/core, an approval from @org/storage satisfies @org/core too, so
// asking @org/storage settles @org/core's files as well as its own. Useful
// counts names; Reach counts what an approval from that team would actually
// do. With no membership the two agree.
func (o *Ownership) Reach(approved OwnerSet, members Membership) []Reach {
	return o.reach(approved, members, nil)
}

// reach is Reach, with ties going to the preferred owners before the rest.
func (o *Ownership) reach(approved OwnerSet, members Membership, prefer OwnerSet) []Reach {
	if members == nil {
		members = NoMembership{}
	}
	remaining := o.Remaining(approved)
	candidates := o.Owners()

	out := make([]Reach, 0, len(candidates))
	for _, candidate := range candidates {
		if approved.Contains(candidate) {
			continue
		}
		stands := standsFor(candidate, candidates, members)
		satisfies := OwnerSet{}
		satisfies.Add(candidate)
		satisfies.Add(stands...)
		n := 0
		for _, path := range remaining {
			if satisfies.ContainsAny(o.owners[path]) {
				n++
			}
		}
		if n > 0 {
			out = append(out, Reach{Owner: candidate, Files: n, StandsFor: stands})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		if pi, pj := prefer.Contains(out[i].Owner), prefer.Contains(out[j].Owner); pi != pj {
			return pi
		}
		return out[i].Owner < out[j].Owner
	})
	return out
}

// Plan suggests approvers to ask for, greedily taking the one that would
// settle the most outstanding files until nothing is left.
//
// Greedy rather than exact. Minimum set cover is NP-hard, the inputs here are
// a handful of owners over a few dozen files, and being one approver off
// optimal costs a review request. Exactness would cost more to explain than it
// would ever save.
func (o *Ownership) Plan(approved OwnerSet, members Membership) []Owner {
	return o.PlanPreferring(approved, members, nil)
}

// PlanPreferring is Plan, choosing a preferred owner over an equally useful
// one: whoever has already been asked, say, rather than whoever sorts first.
func (o *Ownership) PlanPreferring(approved OwnerSet, members Membership, prefer OwnerSet) []Owner {
	have := approved.Clone()
	var plan []Owner

	for {
		reach := o.reach(have, members, prefer)
		if len(reach) == 0 {
			return plan
		}
		next := reach[0]
		plan = append(plan, next.Owner)
		have.Add(next.Owner)
		have.Add(next.StandsFor...)
	}
}
