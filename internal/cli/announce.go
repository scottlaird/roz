package cli

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

const (
	flagChannel = "channel"
	flagAt      = "at"
)

func newPRAnnounceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "announce <owner/repo#number>",
		Short: "Record by hand that a pull request was announced in Slack",
		Long: "Stands in for Slack sync, which does not exist yet.\n\n" +
			"The announcement is the one signal GitHub cannot supply, and two things\n" +
			"hang off it: send_for_review closes on it, and it freezes the pull\n" +
			"request, which is what makes the amend-versus-new-commit rule a computed\n" +
			"fact rather than something to remember.\n\n" +
			"This writes observed columns, which a person is normally not allowed to\n" +
			"do. It is a separate command rather than an --actor override so that the\n" +
			"exception is one named verb instead of a hole in the rule, and it is\n" +
			"logged as sync:slack-manual so the log never claims Slack reported\n" +
			"something that was typed in.",
		Args: cobra.ExactArgs(1),
		RunE: runPRAnnounce,
	}
	f := cmd.Flags()
	f.String(flagChannel, "", "the Slack channel it was announced in (required)")
	f.String(flagAt, "", "when, as a date or timestamp; defaults to now")
	_ = cmd.MarkFlagRequired(flagChannel)
	return cmd
}

func runPRAnnounce(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	f := cmd.Flags()

	channel, err := f.GetString(flagChannel)
	if err != nil {
		return err
	}
	if channel == "" {
		return fmt.Errorf("--%s cannot be empty", flagChannel)
	}
	at, err := announcedAt(cmd)
	if err != nil {
		return err
	}

	if _, _, err := store.ParsePRKey(args[0]); err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	// The actor is fixed by the command, not taken from a flag: this is the
	// one place a person may write observed columns, and it should be
	// reachable only on purpose.
	return updatePR(ctx, cmd, st, store.ActorSlackManual, args[0],
		func(p *store.PR) error {
			p.AnnouncedAt = sql.NullString{String: at, Valid: true}
			p.AnnouncedChannel = sql.NullString{String: channel, Valid: true}
			return nil
		})
}

// announcedAt resolves --at, defaulting to now.
func announcedAt(cmd *cobra.Command) (string, error) {
	given, err := cmd.Flags().GetString(flagAt)
	if err != nil {
		return "", err
	}
	if given == "" {
		return time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), nil
	}
	return validateTimestamp(flagAt, given)
}

// updatePR is the read-modify-write for a pull request, mirroring
// updateProject but with the actor supplied by the caller rather than a flag.
func updatePR(
	ctx context.Context,
	cmd *cobra.Command,
	st *store.Store,
	actor store.Actor,
	id string,
	change func(*store.PR) error,
) error {
	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	before, err := tx.LoadPR(ctx, id)
	if err != nil {
		return notFoundOr(err, id)
	}

	after := before.Clone()
	if err := change(after); err != nil {
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
