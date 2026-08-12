package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scottlaird/roz/internal/github"
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

// stubChange stands in for GitHub.
type stubChange struct {
	change  github.Change
	err     error
	asked   string
	members map[string][]string
	// membersErr defaults to a read:org failure, which is the common way a real
	// client cannot answer: the scope is not on the token.
	membersErr error
}

func (s *stubChange) Change(_ context.Context, key string) (github.Change, error) {
	s.asked = key
	return s.change, s.err
}

func (s *stubChange) TeamMembers(context.Context, []string) (map[string][]string, error) {
	if s.members != nil {
		return s.members, nil
	}
	if s.membersErr != nil {
		return nil, s.membersErr
	}
	return nil, errors.New(`organisation "org" did not resolve: membership needs read:org`)
}

func withChangeReader(t *testing.T, stub *stubChange) {
	t.Helper()
	previous := newChangeReader
	newChangeReader = func() changeReader { return stub }
	t.Cleanup(func() { newChangeReader = previous })
}

// TestCodeownersFromAPullRequest is the whole question in one command: the
// files, the CODEOWNERS on the base branch, and who has already approved.
func TestCodeownersFromAPullRequest(t *testing.T) {
	withChangeReader(t, &stubChange{change: github.Change{
		Key:            "owner/repo#1",
		BaseRef:        "main",
		Files:          []string{"README.md", "storage/engine.go", "api/v1.proto"},
		Codeowners:     ownersFile,
		CodeownersPath: ".github/CODEOWNERS",
		Approvals:      []string{"bob"},
	}})

	out, err := runCLI(t, "codeowners", "--pr", "owner/repo#1",
		"--team", "org/platform=alice,bob")
	if err != nil {
		t.Fatalf("codeowners returned error: %v", err)
	}
	// The base ref is named, so it is clear which rules were applied.
	if !strings.Contains(out, ".github/CODEOWNERS@main") {
		t.Errorf("the source of the rules is not reported:\n%s", out)
	}
	// bob's approval arrived from GitHub, and expanded to his team.
	if !strings.Contains(out, "@org/platform") {
		t.Errorf("the reported approval did not expand to a team:\n%s", out)
	}
	if !strings.Contains(out, "outstanding  2") {
		t.Errorf("want two files left after platform:\n%s", out)
	}
}

func TestCodeownersPullRequestRejections(t *testing.T) {
	withChangeReader(t, &stubChange{change: github.Change{
		BaseRef: "main", Files: []string{"a.go"},
	}})
	// No CODEOWNERS on the base branch: nobody in particular is required, and
	// saying so beats printing a table about nothing.
	if _, err := runCLI(t, "codeowners", "--pr", "owner/repo#1"); err == nil {
		t.Error("a repository with no CODEOWNERS was accepted")
	}

	withChangeReader(t, &stubChange{change: github.Change{
		BaseRef: "main", Files: []string{"a.go"}, Codeowners: ownersFile,
	}})
	if _, err := runCLI(t, "codeowners", "--pr", "owner/repo#1", "--path", "b.go"); err == nil {
		t.Error("--pr together with --path was accepted")
	}
}

// TestCodeownersAddsToTheApprovalsGitHubReports: naming somebody by hand
// should not discard the approvals already on the pull request.
func TestCodeownersAddsToTheApprovalsGitHubReports(t *testing.T) {
	withChangeReader(t, &stubChange{change: github.Change{
		BaseRef:        "main",
		Files:          []string{"README.md", "storage/engine.go"},
		Codeowners:     ownersFile,
		CodeownersPath: "CODEOWNERS",
		Approvals:      []string{"bob"},
	}})

	out, err := runCLI(t, "codeowners", "--pr", "owner/repo#1",
		"--team", "org/platform=alice,bob", "--approved", "@org/storage")
	if err != nil {
		t.Fatalf("codeowners returned error: %v", err)
	}
	if !strings.Contains(out, "nothing outstanding") {
		t.Errorf("the two approvals together did not cover the change:\n%s", out)
	}
}

// TestCodeownersWhenNothingIsOwned: a CODEOWNERS covering a few specific paths
// in a large repository leaves most changes matching no rule at all. Reporting
// "no single owner covers every file" there is true and reads as a problem,
// and sat oddly beside the "nothing outstanding" that followed it.
func TestCodeownersWhenNothingIsOwned(t *testing.T) {
	owners := writeOwners(t)

	out, err := runCLI(t, "codeowners", "--owners", owners,
		"--path", "vendor/a.go", "--path", "vendor/b.go")
	if err != nil {
		t.Fatalf("codeowners returned error: %v", err)
	}
	if !strings.Contains(out, "no rule matches any of these files") {
		t.Errorf("want the plain answer that nobody is required:\n%s", out)
	}
	if strings.Contains(out, "no single owner covers every file") {
		t.Errorf("a change nobody owns was reported as uncoverable:\n%s", out)
	}
	if strings.Contains(out, "nothing outstanding") {
		t.Errorf("two answers to the same question:\n%s", out)
	}
}

// TestCodeownersReportsUnresolvedTeams: the rest of the answer is correct
// without membership, so it is printed, and the gap is stated rather than
// swallowed — under-resolving membership makes a change look less approved
// than it is.
func TestCodeownersReportsUnresolvedTeams(t *testing.T) {
	withChangeReader(t, &stubChange{change: github.Change{
		BaseRef:        "main",
		Files:          []string{"README.md", "storage/engine.go"},
		Codeowners:     ownersFile,
		CodeownersPath: "CODEOWNERS",
		Approvals:      []string{"bob"},
	}})

	out, err := runCLI(t, "codeowners", "--pr", "owner/repo#1")
	if err != nil {
		t.Fatalf("codeowners returned error: %v", err)
	}
	if !strings.Contains(out, "could not read team membership") {
		t.Errorf("the unresolved membership was not reported:\n%s", out)
	}
	// And the rest of the answer is still there.
	if !strings.Contains(out, "@org/platform") {
		t.Errorf("the ownership answer was withheld over a missing enrichment:\n%s", out)
	}
}

// TestCodeownersUsesResolvedTeams: with membership resolved, the same command
// and no flags expands bob into the teams he belongs to.
func TestCodeownersUsesResolvedTeams(t *testing.T) {
	withChangeReader(t, &stubChange{
		change: github.Change{
			BaseRef:        "main",
			Files:          []string{"README.md", "storage/engine.go"},
			Codeowners:     ownersFile,
			CodeownersPath: "CODEOWNERS",
			Approvals:      []string{"bob"},
		},
		members: map[string][]string{
			"org/platform": {"alice", "bob"},
			"org/storage":  {"alice", "carol"},
		},
	})

	out, err := runCLI(t, "codeowners", "--pr", "owner/repo#1")
	if err != nil {
		t.Fatalf("codeowners returned error: %v", err)
	}
	if strings.Contains(out, "could not read team membership") {
		t.Errorf("membership was resolved but still reported as missing:\n%s", out)
	}
	if !strings.Contains(out, "@org/platform") || !strings.Contains(out, "outstanding  1") {
		t.Errorf("bob's approval did not expand to his team:\n%s", out)
	}
}

// TestCodeownersPrefersAnExplicitTeamMapping: --team is the override, so it
// must not be silently replaced by whatever GitHub says.
func TestCodeownersPrefersAnExplicitTeamMapping(t *testing.T) {
	withChangeReader(t, &stubChange{
		change: github.Change{
			BaseRef: "main", Files: []string{"README.md"},
			Codeowners: ownersFile, CodeownersPath: "CODEOWNERS",
			Approvals: []string{"bob"},
		},
		members: map[string][]string{"org/platform": {"nobody"}},
	})

	out, err := runCLI(t, "codeowners", "--pr", "owner/repo#1",
		"--team", "org/platform=bob")
	if err != nil {
		t.Fatalf("codeowners returned error: %v", err)
	}
	if !strings.Contains(out, "nothing outstanding") {
		t.Errorf("the explicit mapping was overridden by the fetched one:\n%s", out)
	}
}
