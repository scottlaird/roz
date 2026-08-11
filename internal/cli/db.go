package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

const (
	flagOut     = "out"
	flagReplace = "replace"
)

func newDBCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Back up and restore the database",
		Long: "Thin wrappers over what SQLite already does, so the syntax does not\n" +
			"have to be remembered at the moment it is needed.",
	}
	cmd.AddCommand(newDBBackupCmd(), newDBRestoreCmd())
	return cmd
}

func newDBBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Write a consistent copy of the database",
		Long: "`VACUUM INTO`, not a file copy. In WAL mode the committed data is\n" +
			"split between the database and the -wal beside it, so copying the\n" +
			"file alone can catch a torn state — safe most of the time, which is\n" +
			"the worst kind of unsafe for a backup. This is consistent even while\n" +
			"`roz serve` is writing.\n\n" +
			"The default destination is a timestamped file in backups/ beside the\n" +
			"database. Timestamped because SQLite refuses to write over an\n" +
			"existing file, so any fixed name would work once and then start\n" +
			"failing. --out overrides it, and is refused the same way if it names\n" +
			"a file that is already there.",
		Args: cobra.NoArgs,
		RunE: runDBBackup,
	}
	cmd.Flags().String(flagOut, "",
		"where to write it (default: a timestamped file in backups/ beside the database)")
	return cmd
}

func runDBBackup(cmd *cobra.Command, _ []string) error {
	out, err := cmd.Flags().GetString(flagOut)
	if err != nil {
		return err
	}
	dbPath, err := dbPathFrom(cmd)
	if err != nil {
		return err
	}

	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	if out == "" {
		out = store.BackupPath(dbPath, time.Now())
	}
	if err := st.Backup(cmd.Context(), out); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), out)
	return nil
}

func newDBRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <file>",
		Short: "Replace the database with a backup",
		Long: "The file is opened and checked before anything is touched, so a path\n" +
			"that is not a roz database, or is ahead of this build, is refused\n" +
			"rather than half-applied.\n\n" +
			"An existing database is left alone unless --replace is passed, and\n" +
			"even then it is renamed aside rather than deleted — the database\n" +
			"being replaced may well be the reason for the restore, and where it\n" +
			"went is printed.\n\n" +
			"Stop anything holding the database first. SQLite cannot tell a\n" +
			"running process that its file has been replaced, so a `roz serve`\n" +
			"left running would carry on reading the one that is no longer there.",
		Args: cobra.ExactArgs(1),
		RunE: runDBRestore,
	}
	cmd.Flags().Bool(flagReplace, false, "move an existing database aside and restore over it")
	return cmd
}

func runDBRestore(cmd *cobra.Command, args []string) error {
	replace, err := cmd.Flags().GetBool(flagReplace)
	if err != nil {
		return err
	}
	dbPath, err := dbPathFrom(cmd)
	if err != nil {
		return err
	}

	// Deliberately not openStore: the database being restored over may be
	// missing or unreadable, which is a reason to run this rather than a
	// reason to refuse.
	movedAside, err := store.Restore(cmd.Context(), args[0], dbPath, replace)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if movedAside != "" {
		fmt.Fprintf(out, "moved the previous database to %s\n", movedAside)
	}
	fmt.Fprintf(out, "restored %s from %s\n", dbPath, args[0])
	return nil
}
