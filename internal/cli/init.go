package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

// The only place the conventional prefixes appear. Everywhere else reads them
// back from the database they were seeded into.
const (
	defaultProjectPrefix = "TD"
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
	dbPath, err := dbPathFrom(cmd)
	if err != nil {
		return err
	}

	requested, err := requestedPrefixes(cmd)
	if err != nil {
		return err
	}

	result, err := store.Init(dbPath, requested)
	if err != nil {
		return err
	}

	if !result.Created {
		if err := reportIgnoredPrefixes(cmd, dbPath, requested, result.Prefixes); err != nil {
			return err
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), initMessage(dbPath, result))
	return nil
}

// initMessage reports what init did, in the three states it can leave behind.
//
// Three rather than two: migrating an existing database is the whole reason
// to re-run this, and folding it into "already initialised" hid the one thing
// that had changed. Nothing here is a complaint — re-running init is a normal
// way to ask what is there — so the unchanged case states the version too.
func initMessage(dbPath string, r store.InitResult) string {
	prefixes := store.FormatPrefixes(r.Prefixes)
	switch {
	case r.Created:
		return fmt.Sprintf("initialised %s (schema %d, %s)", dbPath, r.To, prefixes)
	case r.From != r.To:
		return fmt.Sprintf("migrated %s: schema %d → %d (%s)", dbPath, r.From, r.To, prefixes)
	default:
		return fmt.Sprintf("%s is up to date (schema %d, %s)", dbPath, r.To, prefixes)
	}
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
func reportIgnoredPrefixes(cmd *cobra.Command, dbPath string, requested, effective map[store.Entity]string) error {
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
