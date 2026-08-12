package codeowners

import (
	"reflect"
	"testing"
)

// overlapping is the shape the reduction exists for: one owner over the whole
// repository, another carving out a directory, and a third on a glob that
// reaches into that directory. No two of the three own the same set, and which
// of them a file needs depends on the last rule that matched it.
const overlapping = `
*               @org/platform
/storage/       @org/storage
**/*.proto      @org/api
`

func ownershipOf(t *testing.T, text string, paths ...string) *Ownership {
	t.Helper()
	file, err := ParseString(text)
	if err != nil {
		t.Fatalf("ParseString() returned error: %v", err)
	}
	return file.Of(paths)
}

func TestLastMatchWins(t *testing.T) {
	file, err := ParseString(overlapping)
	if err != nil {
		t.Fatalf("ParseString() returned error: %v", err)
	}

	for _, tc := range []struct {
		path string
		want []Owner
	}{
		{"README.md", []Owner{"org/platform"}},
		{"storage/engine.go", []Owner{"org/storage"}},
		// The proto rule is last, so it takes the file back off storage —
		// which is the whole reason a flat "these owners are involved" list
		// cannot answer anything.
		{"storage/schema.proto", []Owner{"org/api"}},
		{"api/schema.proto", []Owner{"org/api"}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if got := file.Owners(tc.path); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Owners(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestSoleApproverExists: every file is under the same owner, so one approval
// finishes it. This is the cheap answer worth having before asking anyone.
func TestSoleApproverExists(t *testing.T) {
	o := ownershipOf(t, overlapping, "README.md", "cmd/main.go", "docs/a.md")

	if got, want := o.SoleApprovers(), []Owner{"org/platform"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SoleApprovers() = %v, want %v", got, want)
	}
}

// TestNoSoleApprover: three files under three different owners, so nobody can
// approve it alone however many owners the change mentions.
func TestNoSoleApprover(t *testing.T) {
	o := ownershipOf(t, overlapping,
		"README.md", "storage/engine.go", "storage/schema.proto")

	if got := o.SoleApprovers(); len(got) != 0 {
		t.Errorf("SoleApprovers() = %v, want nobody", got)
	}
	if got, want := len(o.Owners()), 3; got != want {
		t.Errorf("the change mentions %d owners, want %d", got, want)
	}
}

// TestUsefulNarrowsAfterAnApproval is the case that motivated this: your own
// team approves and covers most of the change, and of the owners still named
// only some can move it forward.
func TestUsefulNarrowsAfterAnApproval(t *testing.T) {
	o := ownershipOf(t, overlapping,
		"README.md", "cmd/main.go", "docs/a.md", // platform
		"storage/engine.go",    // storage
		"storage/schema.proto", // api
	)

	// Before anyone approves, all three owners are worth asking, most
	// coverage first.
	before := o.Useful(OwnerSet{})
	if len(before) != 3 || before[0].Owner != "org/platform" || before[0].Files != 3 {
		t.Fatalf("Useful() before = %v, want platform first with three files", before)
	}

	// Platform approves. Two files are left, under two different owners, and
	// platform is no longer worth asking about anything.
	after := o.Useful(NewOwnerSet("@org/platform"))
	if got, want := len(o.Remaining(NewOwnerSet("@org/platform"))), 2; got != want {
		t.Errorf("%d files remain, want %d", got, want)
	}
	if len(after) != 2 {
		t.Fatalf("Useful() after = %v, want two owners", after)
	}
	for _, c := range after {
		if c.Owner == "org/platform" {
			t.Error("platform is still listed as useful after approving")
		}
	}
}

func TestEnoughAndPlan(t *testing.T) {
	o := ownershipOf(t, overlapping,
		"README.md", "storage/engine.go", "storage/schema.proto")

	if o.Enough(NewOwnerSet("@org/platform")) {
		t.Error("one approval was called enough for three owners")
	}

	plan := o.Plan(OwnerSet{})
	if len(plan) != 3 {
		t.Errorf("Plan() = %v, want all three owners", plan)
	}
	if !o.Enough(NewOwnerSet(ownerRefs(plan)...)) {
		t.Error("following the plan still leaves files outstanding")
	}

	// A plan from a partial approval only asks for what is missing.
	rest := o.Plan(NewOwnerSet("@org/platform"))
	if len(rest) != 2 {
		t.Errorf("Plan(platform approved) = %v, want the other two", rest)
	}
}

func ownerRefs(owners []Owner) []string {
	out := make([]string, len(owners))
	for i, owner := range owners {
		out[i] = string(owner)
	}
	return out
}

// TestUnownedFilesDoNotBlock: a file no rule matches needs nobody, so it must
// not narrow the sole-approver answer or appear as outstanding.
func TestUnownedFilesDoNotBlock(t *testing.T) {
	o := ownershipOf(t, "/storage/ @org/storage\n",
		"storage/engine.go", "README.md")

	if got, want := o.Unowned(), []string{"README.md"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Unowned() = %v, want %v", got, want)
	}
	if got, want := o.SoleApprovers(), []Owner{"org/storage"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SoleApprovers() = %v, want %v", got, want)
	}
	if o.Remaining(NewOwnerSet("@org/storage")) != nil {
		t.Error("an unowned file was reported as outstanding")
	}
}

// TestNothingOwnedHasNoSoleApprover: nobody is required, so nobody can be the
// one approver. Returning everybody would invent an approval nothing needs.
func TestNothingOwnedHasNoSoleApprover(t *testing.T) {
	o := ownershipOf(t, "/storage/ @org/storage\n", "README.md", "cmd/main.go")

	if got := o.SoleApprovers(); len(got) != 0 {
		t.Errorf("SoleApprovers() = %v, want nobody", got)
	}
	if !o.Enough(OwnerSet{}) {
		t.Error("a change nobody owns was reported as needing an approval")
	}
}

// TestEmptyOwnersRemoveOwnership: a pattern with no owners is not a malformed
// line, it is how a directory is carved out of a broader rule above it.
func TestEmptyOwnersRemoveOwnership(t *testing.T) {
	file, err := ParseString("*            @org/platform\n/vendor/\n")
	if err != nil {
		t.Fatalf("ParseString() returned error: %v", err)
	}

	if got := file.Owners("vendor/lib/x.go"); len(got) != 0 {
		t.Errorf("Owners(vendor) = %v, want nobody", got)
	}
	if got, want := file.Owners("main.go"), []Owner{"org/platform"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Owners(main.go) = %v, want %v", got, want)
	}
	// The rule matched; it just named nobody. That is a different fact from
	// no rule matching, and the caller can tell them apart.
	if _, ok := file.Match("vendor/lib/x.go"); !ok {
		t.Error("the vendor rule did not match, so nothing carved it out")
	}
}
