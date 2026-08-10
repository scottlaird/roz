package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/store"
)

// flagKind is shared with watch, which filters events by kind.
const (
	flagLabel    = "label"
	flagStarts   = "starts"
	flagEnds     = "ends"
	flagCapacity = "capacity"
	flagID       = "id"
)

func newCalendarCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "calendar",
		Short: "Oncall, PTO and holidays — blocks of time that change what fits",
		Long: "Hand-entered at the weekly review rather than synced: calendar\n" +
			"authentication is not worth the code for a handful of multi-day blocks.\n\n" +
			"--ends is INCLUSIVE. Google's all-day events carry an exclusive end\n" +
			"date, so an entry copied from one by hand is a day long unless the\n" +
			"difference is noticed.",
		Aliases: []string{"cal"},
	}
	cmd.AddCommand(
		newCalendarAddCmd(),
		newCalendarShowCmd(),
		newCalendarSetCmd(),
		newCalendarListCmd(),
	)
	return cmd
}

func newCalendarAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Record an entry and print its id",
		Long: "--ends is INCLUSIVE: an entry that runs Monday to Friday ends on the\n" +
			"Friday, not the Saturday.\n\n" +
			"--capacity is not a yes or no. Oncall is usually reduced rather than\n" +
			"none, and treating it as a block reads a whole week as unavailable when\n" +
			"it is not.\n\n" +
			"The id defaults to <kind>-<starts>, which is short enough to type; pass\n" +
			"--id for something else.",
		Args: cobra.NoArgs,
		RunE: runCalendarAdd,
	}
	addCalendarFieldFlags(cmd)
	_ = cmd.MarkFlagRequired(flagKind)
	_ = cmd.MarkFlagRequired(flagStarts)
	_ = cmd.MarkFlagRequired(flagEnds)
	cmd.Flags().String(flagID, "", "identifier; defaults to <kind>-<starts>")
	addActorFlag(cmd)
	return cmd
}

// calendarFieldFlags are the columns settable from the command line. All of
// them are authored: nothing about a window is observed.
var calendarFieldFlags = []string{flagKind, flagLabel, flagStarts, flagEnds, flagCapacity, flagNote}

func addCalendarFieldFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String(flagKind, "", strings.Join(store.WindowKinds, ", "))
	f.String(flagLabel, "", "what it is, e.g. \"oncall week\" or \"Iceland\"")
	f.String(flagStarts, "", "first day, YYYY-MM-DD")
	f.String(flagEnds, "", "last day, YYYY-MM-DD, INCLUSIVE")
	f.String(flagCapacity, store.CapacityFull,
		strings.Join(store.Capacities, ", ")+"; oncall is usually reduced, not none")
	f.String(flagNote, "", "anything worth remembering about it")
}

func runCalendarAdd(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	f := cmd.Flags()

	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	kind, err := f.GetString(flagKind)
	if err != nil {
		return err
	}
	if err := store.ValidateWindowKind(kind); err != nil {
		return err
	}
	starts, err := calendarDate(cmd, flagStarts)
	if err != nil {
		return err
	}

	w := store.NewCalendarWindow(kind, starts)
	if err := applyCalendarFlags(cmd, w); err != nil {
		return err
	}
	if given, err := f.GetString(flagID); err != nil {
		return err
	} else if given != "" {
		w.ID = given
	}
	if err := checkCalendarDates(w); err != nil {
		return err
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	switch _, err := tx.LoadCalendarWindow(ctx, w.ID); {
	case err == nil:
		return fmt.Errorf("%s already exists; pass --%s for a different one", w.ID, flagID)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}

	if err := tx.Insert(ctx, w); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout(), w.ID)
	return nil
}

// calendarDate reads a date flag and insists it is a plain day.
func calendarDate(cmd *cobra.Command, flag string) (string, error) {
	value, err := cmd.Flags().GetString(flag)
	if err != nil {
		return "", err
	}
	return store.ValidateDate("--"+flag, value)
}

func applyCalendarFlags(cmd *cobra.Command, w *store.CalendarWindow) error {
	f := cmd.Flags()

	if f.Changed(flagKind) {
		v, err := f.GetString(flagKind)
		if err != nil {
			return err
		}
		if err := store.ValidateWindowKind(v); err != nil {
			return err
		}
		w.Kind = v
	}
	if f.Changed(flagLabel) {
		v, err := f.GetString(flagLabel)
		if err != nil {
			return err
		}
		w.Label = v
	}
	if f.Changed(flagStarts) {
		v, err := calendarDate(cmd, flagStarts)
		if err != nil {
			return err
		}
		w.StartsOn = v
	}
	if f.Changed(flagEnds) {
		v, err := calendarDate(cmd, flagEnds)
		if err != nil {
			return err
		}
		w.EndsOn = v
	}
	if f.Changed(flagCapacity) {
		v, err := f.GetString(flagCapacity)
		if err != nil {
			return err
		}
		if err := store.ValidateCapacity(v); err != nil {
			return err
		}
		w.Capacity = v
	}
	if f.Changed(flagNote) {
		v, err := f.GetString(flagNote)
		if err != nil {
			return err
		}
		w.Note = v
	}
	return nil
}

// checkCalendarDates reports the schema's ordering rule before it becomes a
// CHECK violation, and says which end is inclusive while it is relevant.
func checkCalendarDates(w *store.CalendarWindow) error {
	if w.EndsOn == "" {
		return fmt.Errorf("--%s is required", flagEnds)
	}
	if w.EndsOn < w.StartsOn {
		return fmt.Errorf("--%s %s is before --%s %s; --%s is the last day, inclusive",
			flagEnds, w.EndsOn, flagStarts, w.StartsOn, flagEnds)
	}
	return nil
}

func newCalendarSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <id>",
		Short: "Change an entry",
		Long:  "Only the columns given are touched. --ends stays inclusive.",
		Args:  cobra.ExactArgs(1),
		RunE:  runCalendarSet,
	}
	addCalendarFieldFlags(cmd)
	addActorFlag(cmd)
	return cmd
}

func runCalendarSet(cmd *cobra.Command, args []string) error {
	f := cmd.Flags()

	var given bool
	for _, flag := range calendarFieldFlags {
		given = given || f.Changed(flag)
	}
	if !given {
		return fmt.Errorf("nothing to set: pass one of --%s", strings.Join(calendarFieldFlags, ", --"))
	}

	return updateCalendarEntry(cmd, args[0], func(_ context.Context, w *store.CalendarWindow) error {
		if err := applyCalendarFlags(cmd, w); err != nil {
			return err
		}
		return checkCalendarDates(w)
	})
}

func updateCalendarEntry(cmd *cobra.Command, id string, change func(context.Context, *store.CalendarWindow) error) error {
	ctx := cmd.Context()

	actor, err := actorFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	before, err := tx.LoadCalendarWindow(ctx, id)
	if err != nil {
		return notFoundOr(err, id)
	}

	after := before.Clone()
	if err := change(ctx, after); err != nil {
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

func newCalendarShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Print one entry in full",
		Args:  cobra.ExactArgs(1),
		RunE:  runCalendarShow,
	}
	addOutputFlag(cmd)
	return cmd
}

func runCalendarShow(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	tx, err := st.Begin(ctx, store.ActorHuman)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	w, err := tx.LoadCalendarWindow(ctx, args[0])
	if err != nil {
		return notFoundOr(err, args[0])
	}

	if format == outputJSON {
		encoded, err := store.MarshalRecord(w)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}
	return writeRecordDetail(cmd.OutOrStdout(), w)
}

func newCalendarListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Recorded entries, earliest first",
		Args:  cobra.NoArgs,
		RunE:  runCalendarList,
	}
	f := cmd.Flags()
	f.Bool("current", false, "covering today")
	f.Bool("upcoming", false, "not yet over — what the weekly review looks at")
	f.String("on", "", "covering one day, YYYY-MM-DD")
	f.String(flagKind, "", "one of "+strings.Join(store.WindowKinds, ", "))
	addOutputFlag(cmd)
	return cmd
}

func runCalendarList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	format, err := outputFrom(cmd)
	if err != nil {
		return err
	}
	filter, err := calendarFilterFrom(cmd)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	entries, err := st.ListCalendarWindows(ctx, filter)
	if err != nil {
		return err
	}

	if format == outputJSON {
		encoded, err := store.MarshalRecords(entries)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return err
	}
	return writeCalendarTable(cmd.OutOrStdout(), entries)
}

func calendarFilterFrom(cmd *cobra.Command) (store.WindowFilter, error) {
	f := cmd.Flags()

	current, err := f.GetBool("current")
	if err != nil {
		return store.WindowFilter{}, err
	}
	upcoming, err := f.GetBool("upcoming")
	if err != nil {
		return store.WindowFilter{}, err
	}
	kind, err := f.GetString(flagKind)
	if err != nil {
		return store.WindowFilter{}, err
	}
	if kind != "" {
		if err := store.ValidateWindowKind(kind); err != nil {
			return store.WindowFilter{}, err
		}
	}

	filter := store.WindowFilter{Current: current, Upcoming: upcoming, Kind: kind}
	if on, err := f.GetString("on"); err != nil {
		return store.WindowFilter{}, err
	} else if on != "" {
		day, err := store.ValidateDate("--on", on)
		if err != nil {
			return store.WindowFilter{}, err
		}
		filter.On = day
	}
	return filter, nil
}

func writeCalendarTable(out io.Writer, entries []*store.CalendarWindow) error {
	if len(entries) == 0 {
		fmt.Fprintln(out, "no calendar entries")
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tKIND\tFROM\tTO (INCL)\tCAPACITY\tLABEL")
	for _, entry := range entries {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			entry.ID, entry.Kind, entry.StartsOn, entry.EndsOn,
			entry.Capacity, orDash(entry.Label))
	}
	return w.Flush()
}
