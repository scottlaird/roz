package cli

import (
	"context"
	"fmt"
	"strings"
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
		"what to wait for: [prefix/]rule, e.g. '>=1.2', '>=minor+1' or 'api/>=3.6'")
}

// refIntent is what --ref asked for, which is one of two different things.
//
// A version somebody already knows is a wait, checked against every ref as it
// appears. A version relative to wherever the repository has got to — the next
// major, two minors on — is a gate, and a gate cannot become a wait until the
// releases it counts from have been read. So it is recorded as pending and
// resolved against the tags, which is exactly what a pipeline's own gate does;
// this only lets one be written by hand as well.
//
// Which of the two an expression is depends on how it is written and never on
// context: `>=minor+1` is not a parseable constraint and `>=1.5` is not a
// parseable gate.
type refIntent struct {
	wait    *store.RefWait
	pending *store.PendingRef
}

// setActionID names the action, which `action add` does not know until after
// the number is allocated.
func (r *refIntent) setActionID(id string) {
	if r.wait != nil {
		r.wait.ActionID = id
	}
	if r.pending != nil {
		r.pending.ActionID = id
	}
}

// repoID is the repository either form watches.
func (r *refIntent) repoID() string {
	if r.wait != nil {
		return r.wait.RepoID
	}
	return r.pending.RepoID
}

// record writes whichever of the two this is.
func (r *refIntent) record(ctx context.Context, tx *store.Tx) error {
	if r.wait != nil {
		return tx.SetRefWait(ctx, *r.wait)
	}
	return tx.AddPendingRef(ctx, *r.pending)
}

// refIntentFrom reads a ref wait or a release gate off the flags, returning
// nil when neither was given.
//
// Repository and expression together or neither: a repository with nothing to
// wait for would match any ref at all, which is never what somebody meant, and
// an expression with no repository has nothing to match against.
func refIntentFrom(cmd *cobra.Command, actionID string) (*refIntent, error) {
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

	// A gate first, because it is the reading that has to be recognised: a
	// gate stored as a wait is a matcher no ref will ever equal, which fails
	// by waiting for ever rather than by saying anything.
	if _, err := store.ParseRelativeRef(spec); err == nil {
		pending := &store.PendingRef{ActionID: actionID, RepoID: repo, Kind: kind, Spec: spec}
		if err := store.ValidateRefKind(pending.Kind); err != nil {
			return nil, err
		}
		return &refIntent{pending: pending}, nil
	}

	wait := &store.RefWait{
		ActionID: actionID, RepoID: repo, Kind: kind,
		PathPrefix: prefix, Matcher: matcher,
	}
	// Everything that is not a gate is a wait, and a wait's matcher is a
	// constraint or a name. A matcher that is neither, but is shaped like a
	// rule about versions, is a mistyped one rather than a ref called that —
	// so it is refused here rather than stored as a name nothing will match.
	if _, isConstraint := wait.Constraint(); !isConstraint && store.LooksLikeVersionRule(matcher) {
		// A gate-shaped expression gets the gate parser's own complaint, which
		// says which part of it is wrong. Anything else names both forms,
		// since which one was meant is not knowable from a typo.
		if strings.Contains(matcher, "+") {
			_, err := store.ParseRelativeRef(spec)
			return nil, err
		}
		return nil, fmt.Errorf(
			"%q is neither a version constraint nor a release gate: "+
				"name a version, e.g. '>=1.2', or count from the current one, e.g. '>=minor+1'",
			spec)
	}
	if err := wait.Validate(); err != nil {
		return nil, err
	}
	return &refIntent{wait: wait}, nil
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
			"A version can also be counted from wherever the repository has got\n" +
			"to, for the common case of waiting for the next release rather than\n" +
			"for one you can name:\n\n" +
			"  roz action wait-ref --action NA103 --ref-repo acme/api --ref '>=minor+1'\n\n" +
			"major, minor and patch all count, and the offset is how many on:\n" +
			"'>=major+1' is the next major, '>=minor+2' two minor lines on. The\n" +
			"version is worked out once, from the highest release already tagged\n" +
			"in the series, and then fixed — so a release that ships in the\n" +
			"meantime does not push the target out.\n\n" +
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
	intent, err := refIntentFrom(cmd, id)
	if err != nil {
		return err
	}

	return withActionTxThen(cmd,
		func(ctx context.Context, tx *store.Tx) error {
			a, err := loadAction(ctx, tx, id)
			if err != nil {
				return err
			}
			if _, err := tx.LoadGitHubRepo(ctx, intent.repoID()); err != nil {
				return notFoundOr(err, intent.repoID())
			}
			if err := intent.record(ctx, tx); err != nil {
				return err
			}
			if intent.wait != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s waits for %s\n", a.ID, intent.wait.Describe())
			}
			return nil
		},
		// The ref may already exist: a wait written after the release was cut
		// is satisfied the moment it is recorded, not at the next poll. On a
		// repository whose refs have never been read this settles nothing and
		// sync does it.
		//
		// A gate is the same case one step earlier — it needs the releases
		// read before it has a version at all — so it is resolved here for the
		// same reason, and left to sync when the repository is unread.
		func(ctx context.Context, st *store.Store) error {
			resolved, err := resolveGateNow(ctx, st, intent)
			if err != nil {
				return err
			}
			reportGate(cmd, intent, resolved)
			return settleActionNow(ctx, cmd, st, id)
		})
}

// resolveGateNow gives a release gate its version, when the repository's
// releases have already been read.
//
// Returns nothing when they have not, which is not a failure: the gate stands,
// sync reads the tags precisely because something is pending on them, and that
// poll resolves it.
func resolveGateNow(ctx context.Context, st *store.Store, intent *refIntent) (*store.Resolved, error) {
	if intent == nil || intent.pending == nil {
		return nil, nil
	}
	return st.ResolvePendingRef(ctx, *intent.pending)
}

// reportGate says what a gate came out as.
//
// Worth a line wherever one is written: the version is roz's to work out
// rather than the caller's, and nothing else shows it. Both commands print it
// after the identifier, which is where a settled action's news already goes.
func reportGate(cmd *cobra.Command, intent *refIntent, resolved *store.Resolved) {
	if intent == nil || intent.pending == nil {
		return
	}
	out := cmd.OutOrStdout()
	if resolved == nil {
		fmt.Fprintf(out, "%s waits for %s %s %s, once its releases have been read\n",
			intent.pending.ActionID, intent.pending.RepoID,
			intent.pending.Kind, intent.pending.Spec)
		return
	}
	fmt.Fprintf(out, "%s waits for %s (%s)\n",
		resolved.ActionID, resolved.Wait.Describe(), resolved.Spec)
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
