package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

func newVerifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify <subject>",
		Short: "Stamp last_verified_at; changes nothing else",
		Long: "Records that an item was checked against reality, as distinct from\n" +
			"when it was last edited. Projects and actions carry the timestamp;\n" +
			"nothing else does.\n\n" +
			"`todo action list --sort staleness` then answers what has gone longest\n" +
			"without anyone looking, which updated_at cannot: every write moves\n" +
			"that one, so an item nobody has touched for a month looks identical\n" +
			"whether it was reviewed on Friday or never.\n\n" +
			"This writes an observed column, which a person is normally not allowed\n" +
			"to do — verifying is asking the world whether the record is still\n" +
			"true, not deciding what it should say. It is a separate command\n" +
			"rather than an --actor override so the exception is one named verb\n" +
			"instead of a hole in the rule, and it is logged as sync:verify so the\n" +
			"log says a person went and looked rather than that something\n" +
			"reported it.",
		Args: cobra.ExactArgs(1),
		RunE: runVerify,
	}
	cmd.Flags().String(flagAt, "", "when it was checked, as a date or timestamp; defaults to now")
	return cmd
}

func runVerify(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	at, err := verifiedAt(cmd)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	// The actor is fixed by the command rather than taken from a flag: this
	// and `pr announce` are the only places a person may write an observed
	// column, and both should be reachable only on purpose.
	tx, err := st.Begin(ctx, store.ActorVerify)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	subject, err := tx.LoadSubject(ctx, st, args[0])
	if err != nil {
		return notFoundOr(err, args[0])
	}

	changes, err := tx.Verify(ctx, subject, at)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if len(changes) == 0 {
		fmt.Fprintf(out, "%s already verified at %s\n", args[0], at)
		return nil
	}
	fmt.Fprintf(out, "%s verified at %s\n", args[0], at)
	return nil
}

// verifiedAt resolves --at, defaulting to now. Backdating is allowed because
// the check often happened before anyone got round to recording it.
func verifiedAt(cmd *cobra.Command) (string, error) {
	given, err := cmd.Flags().GetString(flagAt)
	if err != nil {
		return "", err
	}
	if given == "" {
		return time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), nil
	}
	return validateTimestamp(flagAt, given)
}
