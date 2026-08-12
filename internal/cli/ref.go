package cli

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

const (
	flagRefRepo = "ref-repo"
	flagRefKind = "ref-kind"
	flagRef     = "ref"
)

// addRefWaitFlags puts the four parts of a ref wait on a command.
//
// The same names on `action add` and on `action wait-ref`, so that moving
// between them is not a second vocabulary to remember. Prefixed --ref- even on
// the command called wait-ref, for the same reason.
func addRefWaitFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String(flagRefRepo, "", "repository whose refs to watch, e.g. acme/api")
	f.String(flagRefKind, store.RefTag, "branch or tag")
	f.String(flagRef, "",
		"what to wait for: [prefix/]constraint, e.g. '>=1.2' or 'api/>=3.6'")
}

// refWaitFrom reads a ref wait off the flags, returning nil when none was
// given.
//
// Repository and expression together or neither: a repository with nothing to
// wait for would match any ref at all, which is never what somebody meant, and
// an expression with no repository has nothing to match against.
func refWaitFrom(cmd *cobra.Command, actionID string) (*store.RefWait, error) {
	f := cmd.Flags()

	repo, err := f.GetString(flagRefRepo)
	if err != nil {
		return nil, err
	}
	spec, err := f.GetString(flagRef)
	if err != nil {
		return nil, err
	}
	if repo == "" && spec == "" {
		return nil, nil
	}
	if repo == "" || spec == "" {
		return nil, fmt.Errorf("--%s and --%s go together: name the repository and what to wait for",
			flagRefRepo, flagRef)
	}
	if _, _, err := store.ParseRepoID(repo); err != nil {
		return nil, err
	}

	kind, err := f.GetString(flagRefKind)
	if err != nil {
		return nil, err
	}
	prefix, matcher, err := store.ParseRefSpec(spec)
	if err != nil {
		return nil, err
	}

	wait := &store.RefWait{
		ActionID: actionID, RepoID: repo, Kind: kind,
		PathPrefix: prefix, Matcher: matcher,
	}
	if err := wait.Validate(); err != nil {
		return nil, err
	}
	return wait, nil
}

func newActionWaitRefCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wait-ref",
		Short: "Say which branch or tag an action is waiting for",
		Long: "Waiting for a release used to be a snooze to a guessed date, which is\n" +
			"wrong in both directions: the action wakes early if the release slips\n" +
			"and sleeps through it if it ships early. This waits for the condition\n" +
			"instead.\n\n" +
			"What to wait for is a semver constraint rather than a name, because\n" +
			"when the block is written nobody knows whether the next release is\n" +
			"v1.5.0 or v1.5.1:\n\n" +
			"  roz action wait-ref --action NA103 --ref-repo acme/api --ref '>=1.5'\n\n" +
			"A path prefix selects one series in a monorepo, and series never\n" +
			"compare across: 'api/>=3.6' waits on api/v3.6.0 and later, while\n" +
			"'>=1.2' waits on a top-level tag and is never satisfied by an api\n" +
			"one, whatever the numbers say.\n\n" +
			"Pre-releases are excluded by the constraint's own rule: '>=1.2' does\n" +
			"not match v1.3.0-rc1, and '>=1.2.0-0' does.\n\n" +
			"An expression that is not a constraint is a literal name or glob,\n" +
			"for a ref no version scheme describes: --ref 'release-1.5'.\n\n" +
			"Sync polls only the refs something is waiting for, so this is also\n" +
			"what makes a repository's tags start being read.",
		Args: cobra.NoArgs,
		RunE: runActionWaitRef,
	}
	cmd.Flags().String(flagAction, "", "action, e.g. NA103 (required)")
	_ = cmd.MarkFlagRequired(flagAction)
	addRefWaitFlags(cmd)
	_ = cmd.MarkFlagRequired(flagRefRepo)
	_ = cmd.MarkFlagRequired(flagRef)
	addActorFlag(cmd)
	return cmd
}

func runActionWaitRef(cmd *cobra.Command, _ []string) error {
	id, err := cmd.Flags().GetString(flagAction)
	if err != nil {
		return err
	}
	wait, err := refWaitFrom(cmd, id)
	if err != nil {
		return err
	}

	return withActionTxThen(cmd,
		func(ctx context.Context, tx *store.Tx) error {
			a, err := loadAction(ctx, tx, id)
			if err != nil {
				return err
			}
			if _, err := tx.LoadGitHubRepo(ctx, wait.RepoID); err != nil {
				return notFoundOr(err, wait.RepoID)
			}
			if err := tx.SetRefWait(ctx, *wait); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s waits for %s\n", a.ID, wait.Describe())
			return nil
		},
		// The ref may already exist: a wait written after the release was cut
		// is satisfied the moment it is recorded, not at the next poll. On a
		// repository whose refs have never been read this settles nothing and
		// sync does it.
		func(ctx context.Context, st *store.Store) error {
			return settleActionNow(ctx, cmd, st, id)
		})
}

func newRefCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ref",
		Short: "Branches and tags roz has observed",
		Long: "Observed, never authored: sync writes these and nothing else does.\n\n" +
			"Only the refs something is waiting for are polled, so this lists what\n" +
			"has been relevant rather than everything a repository holds. A\n" +
			"repository with no outstanding wait is never asked.",
	}
	cmd.AddCommand(newRefListCmd())
	return cmd
}

func newRefListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list [repo]",
		Short: "The refs observed, optionally for one repository",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runRefList,
	}
	addOutputFlag(cmd)
	return cmd
}

func runRefList(cmd *cobra.Command, args []string) error {
	var repo string
	if len(args) == 1 {
		repo = args[0]
	}

	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	refs, err := st.ListRefs(cmd.Context(), repo)
	if err != nil {
		return err
	}

	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	if format == outputJSON {
		encoded, err := store.MarshalRecords(refs)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "REPOSITORY\tKIND\tNAME\tCOMMIT\tFIRST SEEN")
	for _, r := range refs {
		sha := r.CommitSHA
		if len(sha) > 8 {
			sha = sha[:8]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.RepoID, r.Kind, r.Name, sha, shortDate(r.FirstSeen))
	}
	return w.Flush()
}
