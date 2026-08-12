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
// alone — the intersection of the owner sets over every owned file.
//
// This is the first question worth asking and the one a flat owner list cannot
// answer. A change touching two directories may mention five teams and still
// have one person who covers all of it, or mention two and have nobody.
//
// A change with no owned files at all returns nothing rather than everybody:
// nobody is required, so nobody is a sole approver, and saying otherwise would
// invent an approval that is not needed.
func (o *Ownership) SoleApprovers() []Owner {
	var candidates OwnerSet
	for _, path := range o.Files {
		owners := o.owners[path]
		if len(owners) == 0 {
			continue // cannot block, so cannot narrow
		}
		if candidates == nil {
			candidates = OwnerSet{}
			candidates.Add(owners...)
			continue
		}
		candidates = candidates.Intersect(owners)
		if len(candidates) == 0 {
			return nil
		}
	}
	return candidates.Sorted()
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

// Plan suggests approvers to ask for, greedily taking the one covering the
// most outstanding files until nothing is left.
//
// Greedy rather than exact. Minimum set cover is NP-hard, the inputs here are
// a handful of owners over a few dozen files, and being one approver off
// optimal costs a review request. Exactness would cost more to explain than it
// would ever save.
func (o *Ownership) Plan(approved OwnerSet) []Owner {
	have := approved.Clone()
	var plan []Owner

	for {
		useful := o.Useful(have)
		if len(useful) == 0 {
			return plan
		}
		next := useful[0].Owner
		plan = append(plan, next)
		have.Add(next)
	}
}
