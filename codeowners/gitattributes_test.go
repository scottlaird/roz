package codeowners

import "testing"

func TestParseGenerated(t *testing.T) {
	g := ParseGenerated(`# Generated code is collapsed in diffs.
api/gen/** linguist-generated
proto/README.md linguist-generated
*.pb.go linguist-generated=true
hand.pb.go -linguist-generated
vendor linguist-generated
docs/ linguist-generated
*.go text eol=lf
"quoted path.txt" linguist-generated
`)
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"api/gen/infra/v1/service.pb.go", true},
		{"api/gen", false}, // a trailing ** needs something inside
		{"proto/README.md", true},
		{"proto/other/README.md", false}, // anchored at the root
		{"walker/api/v1/types.pb.go", true},
		{"walker/api/v1/hand.pb.go", false}, // a later line unsets it
		{"vendor", true},
		{"vendor/lib/a.go", false}, // a gitattributes pattern names the path itself, not a directory's contents
		{"docs/guide.md", false},   // and a directory pattern names no file
		{"main.go", false},
		{"quoted path.txt", false},
	} {
		if got := g.Is(tc.path); got != tc.want {
			t.Errorf("Is(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestParseGeneratedEmpty(t *testing.T) {
	if ParseGenerated("").Is("api/gen/a.pb.go") {
		t.Error("an empty .gitattributes marks nothing")
	}
}
