package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

const (
	flagReviewPolicy    = "review-policy"
	flagAnnounceChannel = "announce-channel"
	flagDisposition     = "disposition"
)

func newRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Tracked GitHub repositories, keyed owner/name",
		Long: "A repository carries policy a pull request cannot — chiefly whether\n" +
			"review is required at all, which decides what a pull request against it\n" +
			"will need doing to it.",
	}
	cmd.AddCommand(
		newRepoTrackCmd(),
		newRepoShowCmd(),
		newRepoSetCmd(),
		newRepoListCmd(),
	)
	return cmd
}

func newRepoTrackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "track <owner/name>",
		Short: "Start tracking a repository",
		Args:  cobra.ExactArgs(1),
		RunE:  runRepoTrack,
	}
	addRepoPolicyFlags(cmd)
	addActorFlag(cmd)
	return cmd
}

// addRepoPolicyFlags registers the authored columns. The observed ones —
// default branch, merge queue, archived — belong to sync.
func addRepoPolicyFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String(flagReviewPolicy, "", "required or none; unset means not yet stated")
	f.String(flagAnnounceChannel, "", "Slack channel its pull requests are announced in")
	f.String(flagDisposition, "", "e.g. another team's area unless they ask directly")
}

func runRepoTrack(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	owner, name, err := store.ParseRepoID(args[0])
	if err != nil {
		return err
	}
	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	r := store.NewGitHubRepo(owner, name)
	if err := applyRepoFlags(cmd, r); err != nil {
		return err
	}

	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	switch _, err := tx.LoadGitHubRepo(ctx, r.ID); {
	case err == nil:
		return fmt.Errorf("%s is already tracked", r.ID)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}

	if err := tx.Insert(ctx, r); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout(), r.ID)
	return nil
}

func newRepoSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <owner/name>",
		Short: "Change a repository's policy",
		Long: "Only the authored columns. The default branch and the rest are\n" +
			"observed, and belong to sync.\n\n" +
			"An empty value clears a column: --review-policy \"\" returns it to\n" +
			"unstated.",
		Args: cobra.ExactArgs(1),
		RunE: runRepoSet,
	}
	addRepoPolicyFlags(cmd)
	addActorFlag(cmd)
	return cmd
}

func runRepoSet(cmd *cobra.Command, args []string) error {
	f := cmd.Flags()
	if !f.Changed(flagReviewPolicy) && !f.Changed(flagAnnounceChannel) && !f.Changed(flagDisposition) {
		return fmt.Errorf("nothing to set: pass --%s, --%s or --%s",
			flagReviewPolicy, flagAnnounceChannel, flagDisposition)
	}

	return updateRepo(cmd, args[0], func(_ context.Context, _ *store.Tx, r *store.GitHubRepo) error {
		return applyRepoFlags(cmd, r)
	})
}

func applyRepoFlags(cmd *cobra.Command, r *store.GitHubRepo) error {
	f := cmd.Flags()

	if f.Changed(flagReviewPolicy) {
		v, err := f.GetString(flagReviewPolicy)
		if err != nil {
			return err
		}
		if err := validateReviewPolicy(v); err != nil {
			return err
		}
		r.ReviewPolicy = nullString(v)
	}
	if f.Changed(flagAnnounceChannel) {
		v, err := f.GetString(flagAnnounceChannel)
		if err != nil {
			return err
		}
		r.AnnounceChannel = nullString(v)
	}
	if f.Changed(flagDisposition) {
		v, err := f.GetString(flagDisposition)
		if err != nil {
			return err
		}
		r.Disposition = v
	}
	return nil
}

func validateReviewPolicy(v string) error {
	switch v {
	case "", store.ReviewRequired, store.ReviewNone:
		return nil
	default:
		return fmt.Errorf("--%s %q is not recognised: use %s or %s, or an empty value to unset",
			flagReviewPolicy, v, store.ReviewRequired, store.ReviewNone)
	}
}

// updateRepo is the read-modify-write the repo verbs share, mirroring
// updateProject.
func updateRepo(cmd *cobra.Command, id string, change func(context.Context, *store.Tx, *store.GitHubRepo) error) error {
	ctx := cmd.Context()

	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	before, err := tx.LoadGitHubRepo(ctx, id)
	if err != nil {
		return notFoundOr(err, id)
	}

	after := before.Clone()
	if err := change(ctx, tx, after); err != nil {
		return err
	}

	changes, err := tx.Update(ctx, before, after)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if len(changes) == 0 {
		fmt.Fprintf(out, "%s unchanged\n", id)
		return nil
	}
	for _, change := range changes {
		fmt.Fprintf(out, "%s %s\n", id, change)
	}
	return nil
}

func newRepoShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <owner/name>",
		Short: "Print one repository in full",
		Args:  cobra.ExactArgs(1),
		RunE:  runRepoShow,
	}
	addOutputFlag(cmd)
	return cmd
}

func runRepoShow(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	r, err := tx.LoadGitHubRepo(ctx, args[0])
	if err != nil {
		return notFoundOr(err, args[0])
	}

	if format == outputJSON {
		encoded, err := store.MarshalRecord(r)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}
	return writeRecordDetail(cmd.OutOrStdout(), r)
}

func newRepoListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Tracked repositories",
		Args:  cobra.NoArgs,
		RunE:  runRepoList,
	}
	addOutputFlag(cmd)
	return cmd
}

func runRepoList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	repos, err := st.ListGitHubRepos(ctx)
	if err != nil {
		return err
	}

	if format == outputJSON {
		encoded, err := store.MarshalRecords(repos)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}
	return writeRepoTable(cmd.OutOrStdout(), repos)
}

func writeRepoTable(out io.Writer, repos []*store.GitHubRepo) error {
	if len(repos) == 0 {
		fmt.Fprintln(out, "no tracked repositories")
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tREVIEW\tDEFAULT BRANCH\tANNOUNCE\tDISPOSITION")
	for _, r := range repos {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			r.ID, nullText(r.ReviewPolicy), nullText(r.DefaultBranch),
			nullText(r.AnnounceChannel), orDash(r.Disposition))
	}
	return w.Flush()
}
