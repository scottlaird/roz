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

func newPipelineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pipeline",
		Short: "What closing a verb instantiates",
		Long: "A pipeline is the chain of actions that follows a piece of work:\n" +
			"undraft, announce, wait for review, merge. Repositories differ in\n" +
			"which one they use, so `roz repo set --pipeline` picks per repository\n" +
			"and a newly tracked one takes the first active pipeline listed here.",
	}
	cmd.AddCommand(newPipelineListCmd(), newPipelineShowCmd(),
		newPipelineAddCmd(), newPipelineSetCmd(), newPipelineRetireCmd())
	return cmd
}

func newPipelineListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Print the pipelines and their steps",
		Args:  cobra.NoArgs,
		RunE:  runPipelineList,
	}
	cmd.Flags().Bool("all", false, "include retired pipelines")
	addListingFlags(cmd, pipelineColumns)
	return cmd
}

// pipelineColumns is what `pipeline list` can show.
//
// steps is not a column of action_pipeline — the steps live in their own
// table and the loader fills them in — so it is declared with the name the
// JSON encoder already gives them. That is what keeps `-o json` carrying the
// steps as structure while the table renders the chain.
var pipelineColumns = columnSet[*store.Pipeline]{
	blank: &store.Pipeline{},
	declared: []column[*store.Pipeline]{
		{
			name: "steps", field: "steps",
			render: func(p *store.Pipeline, _ renderContext) string { return stepsCell(p.Steps) },
		},
	},
	defaults: []string{"name", "label", "active", "steps"},
	empty:    "no pipelines",
}

func runPipelineList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	all, err := cmd.Flags().GetBool("all")
	if err != nil {
		return err
	}
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	pipelines, err := st.ListPipelines(ctx, !all)
	if err != nil {
		return err
	}

	return runListing(cmd, pipelineColumns, pipelines, renderContext{})
}

const (
	flagDescription = "description"
	flagSteps       = "steps"
	flagOrder       = "order"
	flagActive      = "active"
)

func newPipelineAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create a pipeline",
		Long: "The chain a repository's pull requests follow. Steps are verbs, in\n" +
			"order, and every one must close on a predicate: a pipeline is what\n" +
			"follows a person's work, so a step waiting on a person would stop it.\n\n" +
			"  roz pipeline add fast --label \"straight to merge\" --steps undraft,merge\n\n" +
			"No steps is legal and means nothing follows, which is what a\n" +
			"repository whose pull requests you only review wants. It is not the\n" +
			"same as a repository with no pipeline set, though both instantiate\n" +
			"nothing today — one is a decision and the other is a gap.\n\n" +
			"--order says where it lands. Last by default, so adding one never\n" +
			"changes what a newly tracked repository takes; --order first makes it\n" +
			"that default, and says so.",
		Args: cobra.ExactArgs(1),
		RunE: runPipelineAdd,
	}
	f := cmd.Flags()
	f.String(flagLabel, "", "how it reads in a listing; defaults to the name")
	f.String(flagDescription, "", "what it is for")
	f.String(flagSteps, "", "verbs in order, comma-separated; empty means none")
	f.String(flagOrder, store.OrderLast, strings.Join(store.PipelineOrders, " or "))
	addActorFlag(cmd)
	return cmd
}

func runPipelineAdd(cmd *cobra.Command, args []string) error {
	f := cmd.Flags()

	label, err := f.GetString(flagLabel)
	if err != nil {
		return err
	}
	description, err := f.GetString(flagDescription)
	if err != nil {
		return err
	}
	order, err := f.GetString(flagOrder)
	if err != nil {
		return err
	}
	if order != store.OrderLast && order != store.OrderFirst {
		return fmt.Errorf("--%s %q is not a position: use %s",
			flagOrder, order, strings.Join(store.PipelineOrders, " or "))
	}
	steps, err := stepsFrom(cmd)
	if err != nil {
		return err
	}

	return withActionTx(cmd, func(ctx context.Context, tx *store.Tx) error {
		if _, err := tx.LoadPipeline(ctx, args[0]); err == nil {
			return fmt.Errorf("%q already exists; use `roz pipeline set` to change it", args[0])
		}

		p := store.NewPipeline(args[0], label)
		p.Description = description
		if err := tx.InsertPipeline(ctx, p, order); err != nil {
			return err
		}
		if err := tx.SetSteps(ctx, p, steps); err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "%s: %s\n", p.Name, stepsText(p.Steps))
		if order == store.OrderFirst {
			fmt.Fprintf(out, "  newly tracked repositories will take it\n")
		}
		return nil
	})
}

func newPipelineSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Change a pipeline's label, description or steps",
		Long: "Only the flags given are touched.\n\n" +
			"--steps replaces the chain rather than editing it: a chain is short\n" +
			"and ordered, and \"the steps are now these\" is the only edit anybody\n" +
			"makes. --steps \"\" leaves a pipeline that instantiates nothing.\n\n" +
			"Chains already running are unaffected. Steps are copied into actions\n" +
			"when a chain starts, so nothing reads a pipeline again afterwards.",
		Args: cobra.ExactArgs(1),
		RunE: runPipelineSet,
	}
	f := cmd.Flags()
	f.String(flagLabel, "", "how it reads in a listing")
	f.String(flagDescription, "", "what it is for")
	f.String(flagSteps, "", "verbs in order, comma-separated; empty means none")
	addActorFlag(cmd)
	return cmd
}

func runPipelineSet(cmd *cobra.Command, args []string) error {
	f := cmd.Flags()

	return withActionTx(cmd, func(ctx context.Context, tx *store.Tx) error {
		before, err := loadPipeline(ctx, tx, args[0])
		if err != nil {
			return err
		}

		after := before.Clone()
		if f.Changed(flagLabel) {
			if after.Label, err = f.GetString(flagLabel); err != nil {
				return err
			}
		}
		if f.Changed(flagDescription) {
			if after.Description, err = f.GetString(flagDescription); err != nil {
				return err
			}
		}

		changes, err := tx.Update(ctx, before, after)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		for _, change := range changes {
			fmt.Fprintf(out, "%s %s\n", after.Name, change)
		}

		if f.Changed(flagSteps) {
			steps, err := stepsFrom(cmd)
			if err != nil {
				return err
			}
			if err := tx.SetSteps(ctx, after, steps); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s: %s\n", after.Name, stepsText(after.Steps))
		}
		if len(changes) == 0 && !f.Changed(flagSteps) {
			fmt.Fprintf(out, "%s unchanged\n", after.Name)
		}
		return nil
	})
}

func newPipelineRetireCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retire <name>",
		Short: "Take a pipeline out of use for newly tracked repositories",
		Long: "Retired rather than deleted, the way a verb is: a repository may\n" +
			"still name it, and its chains still instantiate. What changes is that\n" +
			"a newly tracked repository will not take it.\n\n" +
			"Repositories already on it are listed, since that is usually the\n" +
			"reason for retiring one.\n\n" +
			"--active restores it.",
		Args: cobra.ExactArgs(1),
		RunE: runPipelineRetire,
	}
	cmd.Flags().Bool(flagActive, false, "put it back into use instead")
	addActorFlag(cmd)
	return cmd
}

func runPipelineRetire(cmd *cobra.Command, args []string) error {
	active, err := cmd.Flags().GetBool(flagActive)
	if err != nil {
		return err
	}

	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	users, err := st.PipelineUsers(cmd.Context(), args[0])
	if err != nil {
		return err
	}

	return withActionTx(cmd, func(ctx context.Context, tx *store.Tx) error {
		before, err := loadPipeline(ctx, tx, args[0])
		if err != nil {
			return err
		}
		after := before.Clone()
		after.Active = active

		changes, err := tx.Update(ctx, before, after)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if len(changes) == 0 {
			fmt.Fprintf(out, "%s is already %s\n", after.Name, activeWord(active))
			return nil
		}
		fmt.Fprintf(out, "%s is %s\n", after.Name, activeWord(active))
		for _, repo := range users {
			fmt.Fprintf(out, "  %s still names it, and keeps working\n", repo)
		}
		return nil
	})
}

func activeWord(active bool) string {
	if active {
		return "in use"
	}
	return "retired"
}

func newPipelineShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Print one pipeline, its steps and what uses it",
		Args:  cobra.ExactArgs(1),
		RunE:  runPipelineShow,
	}
	addOutputFlag(cmd)
	return cmd
}

func runPipelineShow(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

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

	p, err := loadPipeline(ctx, tx, args[0])
	if err != nil {
		return err
	}
	tx.Rollback()

	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	if format == outputJSON {
		encoded, err := store.MarshalRecord(p)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}

	users, err := st.PipelineUsers(ctx, p.Name)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s  %s\n", p.Name, p.Label)
	if p.Description != "" {
		fmt.Fprintf(out, "%s\n", p.Description)
	}
	fmt.Fprintf(out, "steps: %s\n", stepsText(p.Steps))
	fmt.Fprintf(out, "state: %s\n", activeWord(p.Active))
	if len(users) > 0 {
		fmt.Fprintf(out, "used by: %s\n", strings.Join(users, ", "))
	}
	return nil
}

// stepsFrom reads --steps as a list of steps, where empty means none rather
// than one step with an empty name.
//
// A step is a verb, or a verb and what it waits for: `wait_ref(>=minor+2)`.
func stepsFrom(cmd *cobra.Command) ([]store.PipelineStep, error) {
	raw, err := cmd.Flags().GetString(flagSteps)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var steps []store.PipelineStep
	for _, text := range store.SplitSteps(raw) {
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("--%s has an empty step in %q", flagSteps, raw)
		}
		step, err := store.ParseStep(text)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, nil
}

// stepsCell renders a chain for a table, where a blank cell would read as
// missing data rather than as a deliberate absence of steps.
func stepsCell(steps []store.PipelineStep) string {
	if len(steps) == 0 {
		return "-"
	}
	return store.StepsText(steps)
}

// stepsText renders a chain, saying so when there is none rather than printing
// an empty line.
func stepsText(steps []store.PipelineStep) string {
	if len(steps) == 0 {
		return "no steps; nothing follows"
	}
	return store.StepsText(steps)
}

func loadPipeline(ctx context.Context, tx *store.Tx, name string) (*store.Pipeline, error) {
	p, err := tx.LoadPipeline(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%q is not a pipeline; see `roz pipeline list`", name)
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}
