package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

const (
	flagOwner       = "owner"
	flagJiraBaseURL = "jira-base-url"
	flagJiraPrefix  = "jira-prefix"
)

// configFieldFlags are the settable columns. All authored: there is nothing
// here for sync to write.
var configFieldFlags = []string{flagOwner, flagJiraBaseURL, flagJiraPrefix}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Settings the database carries, rather than every invocation",
		Long: "The Jira host, the project keys worth linking, and the name on the\n" +
			"page. These are properties of the queue, not of one command, so they\n" +
			"live in the database with everything else and are visible to anything\n" +
			"reading it.\n\n" +
			"There is one row and no way to make a second. Changing a setting is\n" +
			"logged like any other change, so `roz watch` shows it and the log\n" +
			"says when the page started linking somewhere new.\n\n" +
			"Which database to open stays a flag: it cannot be read out of a\n" +
			"database that has not been chosen yet.",
	}
	cmd.AddCommand(newConfigShowCmd(), newConfigSetCmd())
	return cmd
}

func newConfigShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the settings",
		Args:  cobra.NoArgs,
		RunE:  runConfigShow,
	}
	addOutputFlag(cmd)
	return cmd
}

func runConfigShow(cmd *cobra.Command, _ []string) error {
	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	cfg, err := st.Config(cmd.Context())
	if err != nil {
		return err
	}

	if format == outputJSON {
		encoded, err := store.MarshalRecord(cfg)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}
	return writeRecordDetail(cmd.OutOrStdout(), cfg)
}

func newConfigSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Change a setting",
		Long: "Only the settings given are touched, and one event is logged per\n" +
			"column that actually moved — so setting a value it already has writes\n" +
			"nothing.\n\n" +
			"--jira-prefix replaces the whole list rather than adding to it, since\n" +
			"otherwise there would be no way to remove one. Pass it once per key,\n" +
			"or pass --jira-prefix \"\" to link no keys at all.",
		Args: cobra.NoArgs,
		RunE: runConfigSet,
	}
	f := cmd.Flags()
	f.String(flagOwner, "", "whose queue this is; shown on the page")
	f.String(flagJiraBaseURL, "",
		"where a Jira key becomes a link, e.g. https://example.atlassian.net/browse; "+
			"empty renders keys as plain text")
	f.StringArray(flagJiraPrefix, nil,
		"a project key worth linking, e.g. CDSS; repeat for more, and replaces the whole list. "+
			"Without one, no key is linked: the pattern also matches UTF-8 and SHA-256.")
	addActorFlag(cmd)
	return cmd
}

func runConfigSet(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	f := cmd.Flags()

	var given bool
	for _, flag := range configFieldFlags {
		given = given || f.Changed(flag)
	}
	if !given {
		return fmt.Errorf("nothing to set: pass one of --%s", strings.Join(configFieldFlags, ", --"))
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

	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	before, err := tx.LoadConfig(ctx)
	if err != nil {
		return err
	}
	after := before.Clone()
	if err := applyConfigFlags(cmd, after); err != nil {
		return err
	}

	changes, err := tx.SaveConfig(ctx, before, after)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if len(changes) == 0 {
		fmt.Fprintln(out, "config unchanged")
		return nil
	}
	for _, change := range changes {
		fmt.Fprintf(out, "config %s\n", change)
	}
	return nil
}

func applyConfigFlags(cmd *cobra.Command, cfg *store.Config) error {
	f := cmd.Flags()

	if f.Changed(flagOwner) {
		v, err := f.GetString(flagOwner)
		if err != nil {
			return err
		}
		cfg.Owner = v
	}
	if f.Changed(flagJiraBaseURL) {
		v, err := f.GetString(flagJiraBaseURL)
		if err != nil {
			return err
		}
		// Trailing slashes are stripped here rather than at every use, so the
		// stored value is the one the log and `config show` report.
		cfg.JiraBaseURL = strings.TrimSuffix(v, "/")
	}
	if f.Changed(flagJiraPrefix) {
		v, err := f.GetStringArray(flagJiraPrefix)
		if err != nil {
			return err
		}
		if err := cfg.SetPrefixes(v); err != nil {
			return err
		}
	}
	return nil
}
