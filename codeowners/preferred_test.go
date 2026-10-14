package codeowners

import "testing"

// preferredRules is a broad team over everything, a narrower team carving out
// one directory alongside it, and a directory only the broad team owns.
const preferredRules = `
*            @org/platform @org/infra
/engine/     @org/storage @org/core
/core/       @org/core
`

func preferredMembers() Membership {
	return NewStaticMembership(map[string][]string{
		"org/platform": {"alice", "bob"},
		"org/infra":    {"carol"},
		"org/storage":  {"dave"},
		"org/core":     {"dave", "erin"},
	})
}

func TestPreferred(t *testing.T) {
	for _, tc := range []struct {
		name      string
		paths     []string
		approved  []string
		preferred []Owner
		want      Owner
	}{
		{
			name:      "the first preferred owner that owns and settles everything",
			paths:     []string{"engine/a.go", "engine/b.go"},
			preferred: []Owner{"org/storage", "org/core"},
			want:      "org/storage",
		},
		{
			name:  "the list decides between co-owners",
			paths: []string{"main.go"},
			// platform and infra both own main.go.
			preferred: []Owner{"org/infra", "org/platform"},
			want:      "org/infra",
		},
		{
			name: "a nested team settles its parent's files once it owns one",
			// storage owns engine/a.go and its members are all in core,
			// which owns core/x.go.
			paths:     []string{"engine/a.go", "core/x.go"},
			preferred: []Owner{"org/storage"},
			want:      "org/storage",
		},
		{
			name: "a nested team owning none of the files isn't picked",
			// storage could settle core/x.go through core, but owns nothing
			// here, so the list falls through to core.
			paths:     []string{"core/x.go"},
			preferred: []Owner{"org/storage", "org/core"},
			want:      "org/core",
		},
		{
			name: "a preferred owner that can't settle everything is skipped",
			// storage owns engine/a.go but has nothing to do with main.go.
			paths:     []string{"engine/a.go", "main.go"},
			preferred: []Owner{"org/storage", "org/infra"},
			want:      "org/platform",
		},
		{
			name:      "what is already approved is left out",
			paths:     []string{"engine/a.go", "main.go"},
			approved:  []string{"org/platform"},
			preferred: []Owner{"org/storage"},
			want:      "org/storage",
		},
		{
			name:  "no preference: the owner listed first on the most files",
			paths: []string{"engine/a.go", "engine/b.go", "main.go"},
			want:  "org/storage",
		},
		{
			name:  "a tie in the fallback goes to the name that sorts first",
			paths: []string{"engine/a.go", "main.go"},
			want:  "org/platform",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := ownershipOf(t, preferredRules, tc.paths...)
			got, ok := o.Preferred(NewOwnerSet(tc.approved...), tc.preferred, preferredMembers())
			if !ok || got != tc.want {
				t.Errorf("Preferred() = %q, %v, want %q", got, ok, tc.want)
			}
		})
	}
}

func TestPreferredNothingOutstanding(t *testing.T) {
	o := ownershipOf(t, preferredRules, "engine/a.go")
	if got, ok := o.Preferred(NewOwnerSet("org/core"), []Owner{"org/storage"}, preferredMembers()); ok {
		t.Errorf("Preferred() = %q, want nothing outstanding", got)
	}
}
