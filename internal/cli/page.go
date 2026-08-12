package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/store"
)

const flagBody = "body"

func newPageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "page",
		Short: "Prose the status page places, keyed by slot",
		Long: "Some of what belongs on a queue page is text nothing can derive: an\n" +
			"introduction, what you have decided matters this week, a standing\n" +
			"caveat. It is authored, it is about no particular item, and it had\n" +
			"nowhere to live.\n\n" +
			"Not `roz note`, which puts a comment in the log against a subject so\n" +
			"that narrative stays out of the record. This is the opposite case:\n" +
			"content that exists in order to be shown.\n\n" +
			"A slot is a place on the page. Writing to one puts prose there;\n" +
			"clearing it takes the prose away, and the page draws nothing rather\n" +
			"than an empty box. The set is closed, so a note can never be filed\n" +
			"under a key nothing renders — which would be invisible rather than\n" +
			"wrong, with no error to say so.",
	}
	cmd.AddCommand(newPageSetCmd(), newPageShowCmd(), newPageListCmd(), newPageClearCmd())
	return cmd
}

func newPageSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <slot>",
		Short: "Write the prose in a slot",
		Long: "Markdown, like every other prose field, and linked the same way: an\n" +
			"introduction naming SL7 links it for the reason a project summary\n" +
			"does.\n\n" +
			"Replaces rather than appends. A slot holds the current text; what it\n" +
			"said before is in the log.\n\n" +
			"--body - reads from stdin, which is how anything longer than a\n" +
			"sentence wants to be written:\n\n" +
			"  roz page set intro --body - <<'MD'\n" +
			"  This week is about the merge queue.\n" +
			"  MD",
		Args: cobra.ExactArgs(1),
		RunE: runPageSet,
	}
	cmd.Flags().String(flagBody, "", "the prose, or - to read it from stdin")
	_ = cmd.MarkFlagRequired(flagBody)
	addActorFlag(cmd)
	return cmd
}

func runPageSet(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	if err := store.ValidateSlot(args[0]); err != nil {
		return err
	}
	body, err := bodyFrom(cmd)
	if err != nil {
		return err
	}
	// Raw HTML is refused by the store, which checks every format:"markdown"
	// field on the way in — the same rule --why and --summary follow.

	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	changes, err := st.SetPageNote(ctx, actor, args[0], body)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if len(changes) == 0 {
		fmt.Fprintf(out, "%s set\n", args[0])
		return nil
	}
	for _, change := range changes {
		fmt.Fprintf(out, "%s %s\n", args[0], change)
	}
	return nil
}

// bodyFrom reads --body, from stdin when it is "-".
func bodyFrom(cmd *cobra.Command) (string, error) {
	body, err := cmd.Flags().GetString(flagBody)
	if err != nil {
		return "", err
	}
	if body != "-" {
		return body, nil
	}
	raw, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return "", fmt.Errorf("reading the body: %w", err)
	}
	return strings.TrimRight(string(raw), "\n"), nil
}

func newPageClearCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clear <slot>",
		Short: "Empty a slot, so the page draws nothing there",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := store.ValidateSlot(args[0]); err != nil {
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

			// Emptied rather than deleted: the row's history is the record of
			// what the page used to say, and dropping it would take that with
			// it. An empty slot renders as nothing either way.
			if _, err := st.SetPageNote(cmd.Context(), actor, args[0], ""); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s cleared\n", args[0])
			return nil
		},
	}
	addActorFlag(cmd)
	return cmd
}

func newPageShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <slot>",
		Short: "Print a slot's prose as written",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := store.ValidateSlot(args[0]); err != nil {
				return err
			}
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer st.Close()

			notes, err := st.PageNotes(cmd.Context())
			if err != nil {
				return err
			}
			if note, ok := notes[args[0]]; ok && note.Body != "" {
				fmt.Fprintln(cmd.OutOrStdout(), note.Body)
			}
			return nil
		},
	}
	addOutputFlag(cmd)
	return cmd
}

func newPageListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "The slots, and what is in them",
		Long: "Every slot, in the order it appears on the page, whether or not it\n" +
			"holds anything. An empty one is not a gap: it is a place prose could\n" +
			"go, and listing it is how anyone discovers that.",
		Args: cobra.NoArgs,
		RunE: runPageList,
	}
	addOutputFlag(cmd)
	return cmd
}

func runPageList(cmd *cobra.Command, _ []string) error {
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	notes, err := st.PageNotes(cmd.Context())
	if err != nil {
		return err
	}

	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	if format == outputJSON {
		ordered := make([]*store.PageNote, 0, len(store.Slots))
		for _, slot := range store.Slots {
			if note, ok := notes[slot]; ok {
				ordered = append(ordered, note)
			}
		}
		encoded, err := store.MarshalRecords(ordered)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SLOT\tSET\tFIRST LINE")
	for _, slot := range store.Slots {
		note, ok := notes[slot]
		if !ok || note.Body == "" {
			fmt.Fprintf(w, "%s\t-\t-\n", slot)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", slot, shortDate(note.UpdatedAt), firstLine(note.Body))
	}
	return w.Flush()
}

// firstLine is enough of a note to recognise it in a listing.
func firstLine(body string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	if len(line) > 60 {
		line = strings.TrimRight(line[:57], " ") + "…"
	}
	return line
}
