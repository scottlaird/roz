package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

const (
	flagSubject = "subject"
	flagNote    = "note"
)

func newNoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "note <subject> <text>",
		Short: "Append a note event to a subject",
		Long: "The note goes in the log, not on the item. That is what keeps status\n" +
			"narrative out of the record — the failure that turned a working\n" +
			"backlog into 5,000 lines of accreted history.",
		Args: cobra.ExactArgs(2),
		RunE: runNote,
	}
	addActorFlag(cmd)
	return cmd
}

func runNote(cmd *cobra.Command, args []string) error {
	return annotate(cmd, args[0], func(ctx annotateContext) error {
		return ctx.tx.Note(ctx.ctx, ctx.subject, args[1])
	})
}

func newExceptionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exception",
		Short: "Record an exception event for a monitor to surface",
		Long: "Changes nothing. Severity is separate from kind so a monitor can watch\n" +
			"for exceptions without knowing the kind vocabulary — see `todo watch\n" +
			"--severity exception`.",
		Args: cobra.NoArgs,
		RunE: runException,
	}
	f := cmd.Flags()
	f.String(flagSubject, "", "subject id, e.g. SL48 (required)")
	f.String(flagKind, "", "e.g. unexpected_review_state (required)")
	f.String(flagNote, "", "free text")
	_ = cmd.MarkFlagRequired(flagSubject)
	_ = cmd.MarkFlagRequired(flagKind)
	addActorFlag(cmd)
	return cmd
}

func runException(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()

	subject, err := f.GetString(flagSubject)
	if err != nil {
		return err
	}
	kind, err := f.GetString(flagKind)
	if err != nil {
		return err
	}
	note, err := f.GetString(flagNote)
	if err != nil {
		return err
	}

	return annotate(cmd, subject, func(ctx annotateContext) error {
		return ctx.tx.Exception(ctx.ctx, ctx.subject, kind, note)
	})
}

// annotateContext is what an annotation needs: the transaction and the
// resolved subject.
type annotateContext struct {
	ctx     context.Context
	tx      *store.Tx
	subject store.Record
}

// annotate resolves an identifier to whatever it names and appends one event.
//
// The subject can be any entity: its prefix decides which, using the registry
// seeded at init, so this needs no per-entity branch.
func annotate(cmd *cobra.Command, id string, write func(annotateContext) error) error {
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

	subject, err := tx.LoadSubject(ctx, st, id)
	if err != nil {
		return notFoundOr(err, id)
	}

	if err := write(annotateContext{ctx: ctx, tx: tx, subject: subject}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "recorded against %s\n", id)
	return nil
}

func newRenderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "render",
		Short: "Regenerate the status page from the database",
		Long:  "The page is a view; the database is the truth.",
		Args:  cobra.NoArgs,
		Run:   stub,
	}
	f := cmd.Flags()
	f.String("template", "", "template path, e.g. next-actions.html.tmpl (required)")
	f.String("out", "-", "output path, or - for stdout")
	_ = cmd.MarkFlagRequired("template")
	return cmd
}
