package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/codeowners"
)

const (
	flagOwnersFile = "owners"
	flagApproved   = "approved"
	flagPath       = "path"
	flagTeam       = "team"
)

func newCodeownersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "codeowners",
		Short: "Work out who has to approve a set of changed files",
		Long: "Reads a CODEOWNERS file and a list of paths, and answers the two\n" +
			"questions a list of owners cannot: whether one approval could cover\n" +
			"the whole change, and — once somebody has approved — which of the\n" +
			"remaining owners would actually move it forward.\n\n" +
			"Paths come from stdin, one per line, which is what `gh` already\n" +
			"produces:\n\n" +
			"  gh pr diff 123 --name-only | roz codeowners --owners CODEOWNERS\n\n" +
			"--path takes them as arguments instead, which is how anything without\n" +
			"a shell has to pass them.\n\n" +
			"--approved takes owners, not reviewers, because resolving a login to\n" +
			"the teams it approves for needs the GitHub API. --team supplies that\n" +
			"mapping by hand where it matters: --team org/platform=alice,bob makes\n" +
			"--approved alice satisfy the team as well as the person.\n\n" +
			"Nothing here reads the database. It is the library behind review\n" +
			"routing, exposed so it can be pointed at a real change today.",
		Args: cobra.NoArgs,
		RunE: runCodeowners,
	}
	f := cmd.Flags()
	f.String(flagOwnersFile, "CODEOWNERS", "path to the CODEOWNERS file")
	f.StringSlice(flagApproved, nil, "owners or reviewers who have already approved")
	f.StringArray(flagTeam, nil, "team membership, as org/team=login,login; repeatable")
	// stdin is the natural way to pipe a file list, and the one way an agent
	// calling this over MCP cannot use.
	f.StringArray(flagPath, nil, "a changed path; repeatable, and read from stdin when not given")
	return cmd
}

func runCodeowners(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()

	path, err := f.GetString(flagOwnersFile)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	owners, err := codeowners.ParseString(string(raw))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	paths, err := f.GetStringArray(flagPath)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		if paths, err = readPaths(cmd.InOrStdin()); err != nil {
			return err
		}
	}
	if len(paths) == 0 {
		return fmt.Errorf("no paths: pass --%s, or pipe a file list, e.g. `gh pr diff N --name-only`",
			flagPath)
	}

	teams, err := teamsFrom(cmd)
	if err != nil {
		return err
	}
	approvedBy, err := f.GetStringSlice(flagApproved)
	if err != nil {
		return err
	}
	approved := codeowners.Approval(approvedBy, teams)

	return reportOwnership(cmd.OutOrStdout(), owners.Of(paths), approved, len(approvedBy) > 0)
}

// readPaths takes the file list off stdin, one per line.
func readPaths(in io.Reader) ([]string, error) {
	var paths []string
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			paths = append(paths, line)
		}
	}
	return paths, scanner.Err()
}

// teamsFrom reads the --team flags into a membership table.
func teamsFrom(cmd *cobra.Command) (codeowners.Teams, error) {
	given, err := cmd.Flags().GetStringArray(flagTeam)
	if err != nil {
		return nil, err
	}
	if len(given) == 0 {
		return codeowners.NoTeams{}, nil
	}

	members := map[string][]string{}
	for _, entry := range given {
		team, logins, found := strings.Cut(entry, "=")
		if !found || strings.TrimSpace(team) == "" || strings.TrimSpace(logins) == "" {
			return nil, fmt.Errorf("--%s %q is not team=login,login", flagTeam, entry)
		}
		members[team] = append(members[team], strings.Split(logins, ",")...)
	}
	return codeowners.NewStaticTeams(members), nil
}

func reportOwnership(out io.Writer, o *codeowners.Ownership,
	approved codeowners.OwnerSet, anyApproved bool) error {

	remaining := o.Remaining(approved)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)

	fmt.Fprintf(w, "files\t%d\n", len(o.Files))
	if unowned := o.Unowned(); len(unowned) > 0 {
		fmt.Fprintf(w, "unowned\t%d\n", len(unowned))
	}

	// The cheap answer first: it is often the only one needed.
	if sole := o.SoleApprovers(); len(sole) > 0 {
		fmt.Fprintf(w, "any one of\t%s\n", joinOwners(sole))
	} else if !anyApproved {
		fmt.Fprintf(w, "any one of\t-\tno single owner covers every file\n")
	}

	if anyApproved {
		fmt.Fprintf(w, "approved as\t%s\n", joinOwners(approved.Sorted()))
		fmt.Fprintf(w, "outstanding\t%d\n", len(remaining))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if len(remaining) == 0 {
		fmt.Fprintln(out, "\nnothing outstanding")
		return nil
	}

	// Who is worth asking, and what each would buy. This is the part that a
	// list of the owners a change mentions cannot tell you.
	fmt.Fprintln(out, "\nwould cover")
	w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, c := range o.Useful(approved) {
		fmt.Fprintf(w, "  %s\t%d of %d\n", c.Owner, c.Files, len(remaining))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if plan := o.Plan(approved); len(plan) > 1 {
		fmt.Fprintf(out, "\nfewest approvals: %s\n", joinOwners(plan))
	}
	return nil
}

func joinOwners(owners []codeowners.Owner) string {
	out := make([]string, len(owners))
	for i, owner := range owners {
		out[i] = owner.String()
	}
	return strings.Join(out, " ")
}
