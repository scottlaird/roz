package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

// oneOwner validates a flag that takes a single owner, and returns it as
// CODEOWNERS would write it.
//
// The vocabulary is CODEOWNERS': `@org/storage` is a team, `@alice` a person,
// and an email address is either. Nothing here checks that the owner exists —
// a team that appears in no rule is a legitimate thing to name, and the point
// of an authored field is that it can say something GitHub does not know.
//
// What it does check is that one owner was given. A comma is the mistake worth
// catching: the observed list is the one with several owners in it, and a flag
// that quietly stored "@org/a,@org/b" as a single owner would match nothing
// and look right.
func oneOwner(flag, raw string) (string, error) {
	owner := strings.TrimSpace(raw)
	if strings.ContainsAny(owner, ", ") {
		return "", fmt.Errorf("--%s takes one owner, not a list: %q", flag, raw)
	}
	// The @ is how CODEOWNERS writes an owner, and how everything that reads
	// one expects it. Accepting a bare `org/storage` and storing it as typed
	// would make two spellings of one owner, which is a comparison that fails
	// silently later.
	if !strings.HasPrefix(owner, "@") && !strings.Contains(owner, "@") {
		owner = "@" + owner
	}
	return owner, nil
}

// asOwner is the same normalisation for a positional argument.
func asOwner(raw string) (string, error) {
	owner := strings.TrimSpace(raw)
	if owner == "" {
		return "", fmt.Errorf("name an owner, e.g. @org/storage")
	}
	if strings.ContainsAny(owner, ", ") {
		return "", fmt.Errorf("name one owner, not a list: %q", raw)
	}
	if !strings.HasPrefix(owner, "@") && !strings.Contains(owner, "@") {
		owner = "@" + owner
	}
	return owner, nil
}

func newOwnerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "owner",
		Short: "Where a group is reached",
		Long: "A channel belongs to the reviewer, not to the repository. A change\n" +
			"touching storage should reach the storage channel whichever repository\n" +
			"it is in, and a monorepo has no single right answer at all.\n\n" +
			"Groups only. A channel is how you reach a group; an individual is\n" +
			"reached by naming them, and giving a person a channel is a different\n" +
			"concept wearing the same word.\n\n" +
			"This is a lookup rather than a rule. Who to ask is CODEOWNERS and\n" +
			"`roz repo prefer`; once that is decided, the channel is a table read.",
	}
	cmd.AddCommand(newOwnerSetCmd(), newOwnerListCmd())
	return cmd
}

func newOwnerSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <owner>",
		Short: "Record the channel a group is reached in",
		Long: "  roz owner set @org/storage --channel '#storage-reviews'\n\n" +
			"--channel \"\" forgets it.\n\n" +
			"Nothing is derived from the name. `@org/storage` and\n" +
			"`#storage-reviews` are different namespaces that happen to correlate,\n" +
			"and turning one into the other by string manipulation is the kind of\n" +
			"rule that works for eleven teams and then quietly does not.",
		Args: cobra.ExactArgs(1),
		RunE: runOwnerSet,
	}
	cmd.Flags().String(flagChannel, "", "the Slack channel, e.g. '#storage-reviews'; empty forgets it")
	_ = cmd.MarkFlagRequired(flagChannel)
	addActorFlag(cmd)
	return cmd
}

func runOwnerSet(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	owner, err := asOwner(args[0])
	if err != nil {
		return err
	}
	channel, err := cmd.Flags().GetString(flagChannel)
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

	out := cmd.OutOrStdout()
	if strings.TrimSpace(channel) == "" {
		if err := st.ClearOwnerChannel(ctx, actor, owner); err != nil {
			return notFoundOr(err, owner)
		}
		fmt.Fprintf(out, "%s is reached by naming them\n", owner)
		return nil
	}

	changes, err := st.SetOwnerChannel(ctx, actor, owner, channel)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		fmt.Fprintf(out, "%s → %s\n", owner, channel)
		return nil
	}
	for _, change := range changes {
		fmt.Fprintf(out, "%s %s\n", owner, change)
	}
	return nil
}

func newOwnerListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Every group with a channel",
		Args:  cobra.NoArgs,
		RunE:  runOwnerList,
	}
	addSortFlag(cmd, ownerColumns)
	addListingFlags(cmd, ownerColumns)
	return cmd
}

// ownerColumns is what `owner list` can show. Two columns and both are the
// point, so the default view is everything but the timestamps.
var ownerColumns = columnSet[*store.OwnerChannel]{
	blank:    &store.OwnerChannel{},
	defaults: []string{"owner", "channel"},
	empty:    "no group has a channel",
}

func runOwnerList(cmd *cobra.Command, _ []string) error {
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	channels, err := st.OwnerChannels(cmd.Context())
	if err != nil {
		return err
	}
	return runListing(cmd, ownerColumns, channels, renderContext{})
}

// resolveChannel answers where an announcement should go when nobody said.
//
// The message is as much the feature as the lookup is. Announcing somewhere by
// habit is what this replaces, and a default that does not say what it chose
// or why is the same habit with fewer keystrokes.
func resolveChannel(ctx context.Context, st *store.Store, key string) (channel, why string, err error) {
	target, considered, err := st.ChannelFor(ctx, key)
	if err != nil {
		return "", "", err
	}
	if target != nil {
		if target.Owner == "" {
			return target.Channel, target.Why, nil
		}
		return target.Channel, fmt.Sprintf("%s, %s", target.Owner, target.Why), nil
	}

	// No answer is an answer, and it says what was looked at. The owners are
	// the useful part: the fix is usually one `roz owner set` away, and
	// without them somebody has to go and read CODEOWNERS to find out which.
	if len(considered) == 0 {
		return "", "", fmt.Errorf(
			"nothing says where %s should be announced: it has no required owners recorded, "+
				"so pass --%s", key, flagChannel)
	}
	return "", "", fmt.Errorf(
		"nothing says where %s should be announced: no channel for %s. "+
			"Record one with `roz owner set %s --%s '#somewhere'`, or pass --%s",
		key, strings.Join(considered, ", "), considered[0], flagChannel, flagChannel)
}
