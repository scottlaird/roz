package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ownersFile = `*            @org/platform
/storage/    @org/storage
**/*.proto   @org/api
/vendor/
`

func writeOwners(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "CODEOWNERS")
	if err := os.WriteFile(path, []byte(ownersFile), 0o600); err != nil {
		t.Fatalf("writing CODEOWNERS: %v", err)
	}
	return path
}

func TestCodeownersReportsWhoIsNeeded(t *testing.T) {
	owners := writeOwners(t)

	out, err := runCLI(t, "codeowners", "--owners", owners,
		"--path", "README.md", "--path", "storage/engine.go", "--path", "api/v1.proto")
	if err != nil {
		t.Fatalf("codeowners returned error: %v", err)
	}
	for _, want := range []string{"@org/platform", "@org/storage", "@org/api", "fewest approvals"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

// TestCodeownersFindsASoleApprover is the cheap answer, and often the only one
// needed: every file under one owner means one review finishes it.
func TestCodeownersFindsASoleApprover(t *testing.T) {
	owners := writeOwners(t)

	out, err := runCLI(t, "codeowners", "--owners", owners,
		"--path", "README.md", "--path", "cmd/main.go")
	if err != nil {
		t.Fatalf("codeowners returned error: %v", err)
	}
	if !strings.Contains(out, "any one of  @org/platform") {
		t.Errorf("no sole approver reported:\n%s", out)
	}
}

// TestCodeownersExpandsAReviewerIntoTheirTeams: a review arrives as a person,
// and the files are owned by teams. Without the expansion a fully approved
// change still reads as outstanding.
func TestCodeownersExpandsAReviewerIntoTheirTeams(t *testing.T) {
	owners := writeOwners(t)
	args := []string{"codeowners", "--owners", owners,
		"--path", "README.md", "--path", "storage/engine.go",
		"--team", "org/platform=alice,bob", "--team", "org/storage=alice,carol"}

	// Alice is in both owning teams, so her single approval finishes it.
	out, err := runCLI(t, append(args, "--approved", "alice")...)
	if err != nil {
		t.Fatalf("codeowners returned error: %v", err)
	}
	if !strings.Contains(out, "nothing outstanding") {
		t.Errorf("alice's approval left something outstanding:\n%s", out)
	}

	// Bob is in one of them, so storage is still wanted — and platform is not
	// listed, because asking them again would achieve nothing.
	out, err = runCLI(t, append(args, "--approved", "bob")...)
	if err != nil {
		t.Fatalf("codeowners returned error: %v", err)
	}
	if !strings.Contains(out, "@org/storage") {
		t.Errorf("storage was not reported as still needed:\n%s", out)
	}
	if strings.Contains(out, "would cover\n  @org/platform") {
		t.Errorf("platform is listed as useful after approving:\n%s", out)
	}
}

func TestCodeownersRejections(t *testing.T) {
	owners := writeOwners(t)

	if _, err := runCLI(t, "codeowners", "--owners", filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("a missing CODEOWNERS file was accepted")
	}
	if _, err := runCLI(t, "codeowners", "--owners", owners,
		"--path", "a.go", "--team", "nonsense"); err == nil {
		t.Error("a malformed --team was accepted")
	}
	// No paths at all, with stdin empty.
	if _, err := runCLI(t, "codeowners", "--owners", owners); err == nil {
		t.Error("running with no paths was accepted")
	}
}
