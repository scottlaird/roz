package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

// The only place the conventional prefixes appear. Everywhere else reads them
// back from the database they were seeded into.
const (
	defaultProjectPrefix = "SL"
	defaultActionPrefix  = "NA"
)

const (
	projectPrefixFlag = "project-prefix"
	actionPrefixFlag  = "action-prefix"
)

// newInitCmd is not in the design sketch's verb list, but the database has to
// come from somewhere.
func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create the database and apply the schema",
		Long: "Creates the database and its parent directory, applies the schema, and\n" +
			"records the identifier prefixes. Safe to re-run: an existing database is\n" +
			"left exactly as it is.\n\n" +
			"Prefixes are chosen once, here. Later commands read them from the\n" +
			"database, and they cannot be changed afterwards without orphaning every\n" +
			"identifier already issued.",
		Args: cobra.NoArgs,
		RunE: runInit,
	}
	f := cmd.Flags()
	f.String(projectPrefixFlag, defaultProjectPrefix, "identifier prefix for projects; letters only, write-once")
	f.String(actionPrefixFlag, defaultActionPrefix, "identifier prefix for actions; letters only, write-once")
	return cmd
}

func runInit(cmd *cobra.Command, _ []string) error {
	if dbPath == "" {
		return errors.New("no database path: pass --db or set TODO_DB")
	}

	requested, err := requestedPrefixes(cmd)
	if err != nil {
		return err
	}

	created, effective, err := store.Init(dbPath, requested)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if created {
		fmt.Fprintf(out, "initialised %s (%s)\n", dbPath, store.FormatPrefixes(effective))
		return nil
	}
	if err := reportIgnoredPrefixes(cmd, requested, effective); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s is already initialised (%s)\n", dbPath, store.FormatPrefixes(effective))
	return nil
}

func requestedPrefixes(cmd *cobra.Command) (map[store.Entity]string, error) {
	project, err := cmd.Flags().GetString(projectPrefixFlag)
	if err != nil {
		return nil, err
	}
	action, err := cmd.Flags().GetString(actionPrefixFlag)
	if err != nil {
		return nil, err
	}
	return map[store.Entity]string{
		store.EntityProject: project,
		store.EntityAction:  action,
	}, nil
}

// reportIgnoredPrefixes fails when the user explicitly asked for a prefix the
// database does not have. Silently ignoring the flag would leave them
// believing a rename had happened.
func reportIgnoredPrefixes(cmd *cobra.Command, requested, effective map[store.Entity]string) error {
	for flag, entity := range map[string]store.Entity{
		projectPrefixFlag: store.EntityProject,
		actionPrefixFlag:  store.EntityAction,
	} {
		if !cmd.Flags().Changed(flag) || requested[entity] == effective[entity] {
			continue
		}
		return fmt.Errorf(
			"%s is already initialised with %s=%s; --%s cannot change it, because identifiers already issued would be orphaned",
			dbPath, entity, effective[entity], flag)
	}
	return nil
}
