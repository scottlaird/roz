package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

const (
	flagPipeline        = "pipeline"
	flagAnnounceChannel = "announce-channel"
	flagShortName       = "short-name"
	flagDisposition     = "disposition"
)

func newRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Tracked GitHub repositories, keyed owner/name",
		Long: "A repository carries policy a pull request cannot — chiefly its\n" +
			"pipeline, which is what happens to a pull request between written and\n" +
			"merged. `roz pipeline list` shows the choices.",
	}
	cmd.AddCommand(
		newRepoTrackCmd(),
		newRepoShowCmd(),
		newRepoSetCmd(),
		newRepoListCmd(),
		newRepoPreferCmd(),
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
	f.String(flagPipeline, "", "how its pull requests reach merge; see `roz pipeline list`")
	f.String(flagAnnounceChannel, "", "Slack channel its pull requests are announced in")
	f.String(flagDisposition, "", "e.g. another team's area unless they ask directly")
	f.String(flagShortName, "",
		"what to call it in prose, so api#1234 becomes a link; unique, and \"\" clears it")
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
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	r := store.NewGitHubRepo(owner, name)
	if err := applyRepoFlags(cmd, r); err != nil {
		return err
	}
	if !cmd.Flags().Changed(flagPipeline) {
		if err := applyDefaultPipeline(ctx, st, r); err != nil {
			return err
		}
	}

	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := checkShortNameFree(ctx, tx, r); err != nil {
		return err
	}

	switch _, err := tx.LoadGitHubRepo(ctx, r.ID); {
	case err == nil:
		return fmt.Errorf("%s is already tracked", r.ID)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}

	if err := checkPipelineUsable(ctx, tx, r.Pipeline); err != nil {
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
			"An empty value clears a column: --pipeline \"\" returns it to\n" +
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
	if !f.Changed(flagPipeline) && !f.Changed(flagAnnounceChannel) &&
		!f.Changed(flagDisposition) && !f.Changed(flagShortName) {
		return fmt.Errorf("nothing to set: pass --%s, --%s, --%s or --%s",
			flagPipeline, flagAnnounceChannel, flagDisposition, flagShortName)
	}

	return updateRepo(cmd, args[0], func(ctx context.Context, tx *store.Tx, r *store.GitHubRepo) error {
		if err := applyRepoFlags(cmd, r); err != nil {
			return err
		}
		if err := checkShortNameFree(ctx, tx, r); err != nil {
			return err
		}
		return checkPipelineUsable(ctx, tx, r.Pipeline)
	})
}

// checkShortNameFree reports a collision as the repository it clashes with.
//
// The unique index is what actually enforces this; the point of asking first
// is the message. "api is already scottlaird/roz" is something a person can
// act on, and a UNIQUE constraint violation is not.
func checkShortNameFree(ctx context.Context, tx *store.Tx, r *store.GitHubRepo) error {
	if !r.ShortName.Valid {
		return nil
	}
	holder, taken, err := tx.ShortNameHolder(ctx, r.ShortName.String)
	if err != nil {
		return err
	}
	if taken && holder != r.ID {
		return fmt.Errorf("the short name %q is already %s", r.ShortName.String, holder)
	}
	return nil
}

func applyRepoFlags(cmd *cobra.Command, r *store.GitHubRepo) error {
	f := cmd.Flags()

	if f.Changed(flagPipeline) {
		v, err := f.GetString(flagPipeline)
		if err != nil {
			return err
		}
		r.Pipeline = nullString(v)
	}
	if f.Changed(flagAnnounceChannel) {
		v, err := f.GetString(flagAnnounceChannel)
		if err != nil {
			return err
		}
		r.AnnounceChannel = nullString(v)
	}
	if f.Changed(flagShortName) {
		v, err := f.GetString(flagShortName)
		if err != nil {
			return err
		}
		if v != "" {
			if err := store.ValidateShortName(v); err != nil {
				return err
			}
		}
		r.ShortName = nullString(v)
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

// applyDefaultPipeline gives a newly tracked repository the lowest-numbered
// active pipeline.
//
// The default is resolved here, at track time, rather than read through a
// NULL later: retiring a pipeline or adding one in front of it should not
// silently change how repositories already tracked behave.
//
// A database with every pipeline retired leaves it unstated. That is someone
// having emptied the table deliberately, and it is not this command's place
// to argue.
func applyDefaultPipeline(ctx context.Context, st *store.Store, r *store.GitHubRepo) error {
	p, err := st.DefaultPipeline(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	r.Pipeline = sql.NullString{String: p.Name, Valid: true}
	return nil
}

// checkPipelineUsable reports an unknown or retired pipeline as itself,
// rather than leaving the foreign key to report a constraint.
func checkPipelineUsable(ctx context.Context, tx *store.Tx, name sql.NullString) error {
	if !name.Valid {
		return nil
	}
	p, err := tx.LoadPipeline(ctx, name.String)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%q is not a pipeline; see `roz pipeline list`", name.String)
	}
	if err != nil {
		return err
	}
	if !p.Active {
		return fmt.Errorf("%q is retired and cannot be used", name.String)
	}
	return nil
}

// updateRepo is the read-modify-write the repo verbs share.
func updateRepo(cmd *cobra.Command, id string, change func(context.Context, *store.Tx, *store.GitHubRepo) error) error {
	_, err := updateRecord(cmd, id, (*store.Tx).LoadGitHubRepo, change)
	return err
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
	st, err := openStore(cmd)
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
	addSortFlag(cmd, repoColumns)
	addListingFlags(cmd, repoColumns)
	return cmd
}

// repoColumns is what `repo list` can show.
//
// SHORT is in the default view for the reason WAIT is on `verb list`: it is a
// setting, and one that cannot be read is one nobody can manage.
var repoColumns = columnSet[*store.GitHubRepo]{
	blank: &store.GitHubRepo{},
	declared: []column[*store.GitHubRepo]{
		{name: "short_name", header: "SHORT"},
		{name: "announce_channel", header: "ANNOUNCE"},
	},
	defaults: []string{
		"id", "short_name", "pipeline", "default_branch",
		"announce_channel", "disposition",
	},
	empty: "no tracked repositories",
}

func runRepoList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	_, sort, err := sortFrom(cmd, repoColumns)
	if err != nil {
		return err
	}
	// Into the query rather than over the rows: whatever SQLite can take, it
	// takes, and runListing runs what is left. See #201.
	cel, err := filterFrom(cmd, &store.GitHubRepo{})
	if err != nil {
		return err
	}
	var pushed store.SQLWhere
	pushed.Where, pushed.WhereArgs = cel.Take()
	cel.WithLoader(cmd.Context(), storeLoader{st: st})

	repos, err := st.ListGitHubRepos(ctx, sort, pushed)
	if err != nil {
		return err
	}

	return runListing(cmd, repoColumns, repos, renderContext{})
}

const flagPrefer = "prefer"

func newRepoPreferCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prefer <repo>",
		Short: "The owners to try first when routing a review",
		Long: "`roz codeowners` can say who could approve a change. This is how a\n" +
			"repository says who to *ask*, so the routing stops being folklore.\n\n" +
			"  roz repo prefer acme/api --prefer @org/platform,@org/storage\n\n" +
			"An ordered preference, checked rather than trusted: a hint that owns\n" +
			"nothing in a particular change is skipped rather than asked, and one\n" +
			"that owns everything makes the later tiers unnecessary. That check is\n" +
			"what stops a hint becoming a habit nobody revisits.\n\n" +
			"A hint need not appear in CODEOWNERS at all. A team whose members all\n" +
			"belong to an owning team is usable, because the approval it produces\n" +
			"satisfies the rule — which is a question about membership rather than\n" +
			"names, and needs GitHub to answer.\n\n" +
			"Hints never override CODEOWNERS. Ordering what is already required is\n" +
			"safe; substituting for a required owner is not.\n\n" +
			"--prefer \"\" clears them.",
		Args: cobra.ExactArgs(1),
		RunE: runRepoPrefer,
	}
	cmd.Flags().String(flagPrefer, "", "owners in order, comma-separated; empty clears them")
	_ = cmd.MarkFlagRequired(flagPrefer)
	addActorFlag(cmd)
	return cmd
}

func runRepoPrefer(cmd *cobra.Command, args []string) error {
	raw, err := cmd.Flags().GetString(flagPrefer)
	if err != nil {
		return err
	}

	var hints []string
	if strings.TrimSpace(raw) != "" {
		for _, owner := range strings.Split(raw, ",") {
			owner = strings.TrimSpace(owner)
			if owner == "" {
				return fmt.Errorf("--%s has an empty owner in %q", flagPrefer, raw)
			}
			hints = append(hints, owner)
		}
	}

	return withActionTx(cmd, func(ctx context.Context, tx *store.Tx) error {
		repo, err := tx.LoadGitHubRepo(ctx, args[0])
		if err != nil {
			return notFoundOr(err, args[0])
		}
		if err := tx.SetOwnerHints(ctx, repo, hints); err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		if len(hints) == 0 {
			fmt.Fprintf(out, "%s prefers nobody in particular\n", repo.ID)
			return nil
		}
		fmt.Fprintf(out, "%s prefers %s\n", repo.ID, strings.Join(hints, " → "))
		return nil
	})
}
