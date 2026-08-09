package cli

import "github.com/spf13/cobra"

func newActionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "action",
		Short: "One thing to do next, allocated an NA identifier",
	}
	cmd.AddCommand(
		newActionAddCmd(),
		newActionAddBlockerCmd(),
		newActionHideBehindCmd(),
		newActionLinkPRCmd(),
		newActionCloseCmd(),
		newActionSnoozeCmd(),
		newActionWakeCmd(),
		newActionListCmd(),
	)
	return cmd
}

func newActionAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Allocate a new action and print its id",
		Args:  cobra.NoArgs,
		Run:   stub,
	}
	f := cmd.Flags()
	f.String("title", "", "what to do (required)")
	f.String("project", "", "project this advances, e.g. SL200; omit if it advances nothing")
	f.String("verb", "", "from the actionverb vocabulary, e.g. write (required)")
	f.String("why", "", "what it unblocks; one sentence maximum")
	_ = cmd.MarkFlagRequired("title")
	_ = cmd.MarkFlagRequired("verb")
	return cmd
}

func newActionAddBlockerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add-blocker",
		Short: "Record that one action must precede another",
		Args:  cobra.NoArgs,
		Run:   stub,
	}
	f := cmd.Flags()
	f.String("from", "", "blocked action (required)")
	f.String("to", "", "action that blocks it (required)")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func newActionHideBehindCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hide-behind",
		Short: "Fold an action out of the queue until another clears",
		Long: "Not the same edge as add-blocker. Blocked-by is a fact about ordering;\n" +
			"hidden-behind is the judgement that there is nothing to do about this\n" +
			"one other than clearing the other.",
		Args: cobra.NoArgs,
		Run:  stub,
	}
	f := cmd.Flags()
	f.String("action", "", "action to hide (required)")
	f.String("behind", "", "action it hides behind (required)")
	_ = cmd.MarkFlagRequired("action")
	_ = cmd.MarkFlagRequired("behind")
	return cmd
}

func newActionLinkPRCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link-pr",
		Short: "Attach a pull request to an action",
		Long: "An action has at most one subject PR; use --role context to mention a\n" +
			"pull request for background without making it the subject.",
		Args: cobra.NoArgs,
		Run:  stub,
	}
	f := cmd.Flags()
	f.String("action", "", "action, e.g. NA103 (required)")
	f.String("pr", "", "pull request, e.g. myrepo#812 (required)")
	f.String("role", "subject", "subject or context")
	_ = cmd.MarkFlagRequired("action")
	_ = cmd.MarkFlagRequired("pr")
	return cmd
}

func newActionCloseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "close <action>",
		Short: "Close an action; cascades to its dependents",
		Args:  cobra.ExactArgs(1),
		Run:   stub,
	}
	f := cmd.Flags()
	f.String("reason", "completed", "completed, superseded, dropped or obsolete")
	f.String("pr", "", "link a pull request as part of closing")
	return cmd
}

func newActionSnoozeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snooze <action>",
		Short: "Defer an action to a real date",
		Long:  `--until must be a timestamp. "Next week" is not one, and that is the point.`,
		Args:  cobra.ExactArgs(1),
		Run:   stub,
	}
	f := cmd.Flags()
	f.String("until", "", "ISO-8601 date or timestamp (required)")
	f.String("reason", "", "why it is deferred")
	_ = cmd.MarkFlagRequired("until")
	return cmd
}

func newActionWakeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "wake <action>",
		Short: "Clear a snooze and return an action to the queue",
		Args:  cobra.ExactArgs(1),
		Run:   stub,
	}
}

func newActionListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "The queue",
		Args:  cobra.NoArgs,
		Run:   stub,
	}
	f := cmd.Flags()
	f.Bool("unblocked", false, "ready, not hidden, not snoozed — the queue")
	f.Bool("expired", false, "snoozes whose date has passed")
	f.Bool("stale", false, "verb predicate and state disagree, in either direction")
	f.Bool("waiting", false, "waiting on someone, sorted by waiting_since")
	f.Bool("orphaned", false, "projects with no open action and no snooze")
	return cmd
}
