package cli

import (
	"database/sql"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scottlaird/roz/internal/markdown"
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
			"go, and listing it is how anyone discovers that.\n\n" +
			"That is why this listing has no --sort. The rows are the slots and\n" +
			"the order is where they sit on the page, which is the only order that\n" +
			"means anything; sorting them by when they were written would be a\n" +
			"list of the same four things in an order nobody reads them in.\n\n" +
			"A slot nobody has written to has no row behind it, so its columns are\n" +
			"empty rather than absent — including created_at, which is how -o json\n" +
			"says the same thing the table says with a dash.",
		Args: cobra.NoArgs,
		RunE: runPageList,
	}
	addListingFlags(cmd, pageColumns)
	return cmd
}

// pageColumns is the slot listing.
//
// first_line is derived: a note is Markdown and can be a page of it, so what a
// listing wants is enough to recognise it by. body is there under --fields for
// anything that wants the whole thing.
var pageColumns = columnSet[*store.PageNote]{
	blank: &store.PageNote{},
	declared: []column[*store.PageNote]{
		{name: "key", header: "SLOT"},
		{name: "updated_at", header: "SET",
			render: func(n *store.PageNote, _ renderContext) string {
				return dateCell(sql.NullString{String: n.UpdatedAt, Valid: n.UpdatedAt != ""})
			}},
		// A note is Markdown and can be paragraphs of it, so the table gets
		// it on one line — a cell with a newline in it is not a cell. -o json
		// and -o csv keep the source, which is what a consumer wants.
		{name: "body",
			render: func(n *store.PageNote, _ renderContext) string {
				return markdown.Line(n.Body)
			}},
		{name: "first_line", header: "FIRST LINE",
			render: func(n *store.PageNote, _ renderContext) string {
				return firstLine(n.Body)
			}},
	},
	defaults: []string{"key", "updated_at", "first_line"},
	empty:    "no slots",
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

	// The rows are the slots, so a slot with nothing in it gets a record with
	// nothing in it. #192 asked whether to do this or to let a row carry an
	// absent record, and this is the cleaner of the two: the alternative
	// teaches the whole marshalling path about a nil record for one listing's
	// sake, and what it would then emit into a JSON array is `null`, which is
	// worse for a reader than a slot with an empty body.
	//
	// It also settles a disagreement. The table listed every slot and -o json
	// listed only the written ones, so the two formats did not agree about
	// what this listing contains. They do now.
	rows := make([]*store.PageNote, 0, len(store.Slots))
	for _, slot := range store.Slots {
		if note, ok := notes[slot]; ok {
			rows = append(rows, note)
			continue
		}
		rows = append(rows, &store.PageNote{Key: slot})
	}

	return runListing(cmd, pageColumns, rows, renderContext{})
}

// firstLine is enough of a note to recognise it in a listing.
func firstLine(body string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	if len(line) > 60 {
		line = strings.TrimRight(line[:57], " ") + "…"
	}
	return line
}
