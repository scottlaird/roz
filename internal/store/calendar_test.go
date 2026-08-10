package store

import (
	"context"
	"testing"
)

// addCalendarEntry records one window and commits it.
func addCalendarEntry(t *testing.T, st *Store, kind, starts, ends, capacity string) *CalendarWindow {
	t.Helper()
	ctx := context.Background()

	w := NewCalendarWindow(kind, starts)
	w.EndsOn = ends
	w.Capacity = capacity

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.Insert(ctx, w); err != nil {
		t.Fatalf("Insert() returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	return w
}

func TestWindowIDIsReadable(t *testing.T) {
	w := NewCalendarWindow(WindowOncall, "2026-08-10")
	if want := "oncall-2026-08-10"; w.ID != want {
		t.Errorf("id = %q, want %q", w.ID, want)
	}
	if w.Capacity != CapacityFull {
		t.Errorf("capacity = %q, want the least surprising default %q", w.Capacity, CapacityFull)
	}
}

// TestEndsOnIsInclusive is the trap the sketch calls out: Google's all-day
// events carry an exclusive end date, so a window copied by hand is a day
// short unless both ends count.
func TestEndsOnIsInclusive(t *testing.T) {
	w := &CalendarWindow{StartsOn: "2026-08-10", EndsOn: "2026-08-16"}

	tests := []struct {
		day  string
		want bool
	}{
		{day: "2026-08-09", want: false},
		{day: "2026-08-10", want: true}, // first day counts
		{day: "2026-08-13", want: true},
		{day: "2026-08-16", want: true}, // and so does the last
		{day: "2026-08-17", want: false},
	}
	for _, tt := range tests {
		if got := w.Covers(tt.day); got != tt.want {
			t.Errorf("Covers(%s) = %v, want %v", tt.day, got, tt.want)
		}
	}
}

func TestValidateDate(t *testing.T) {
	tests := []struct {
		value   string
		wantErr bool
	}{
		{value: "2026-08-10"},
		{value: "2026-02-29", wantErr: true}, // 2026 is not a leap year
		{value: "", wantErr: true},
		{value: "10/08/2026", wantErr: true},
		{value: "2026-8-10", wantErr: true},
		// A timestamp is refused rather than truncated: these are whole days.
		{value: "2026-08-10T00:00:00Z", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			_, err := ValidateDate("--starts", tt.value)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Errorf("ValidateDate(%q) error = %v, want error %v", tt.value, err, tt.wantErr)
			}
		})
	}
}

func TestValidateEnums(t *testing.T) {
	if err := ValidateWindowKind(WindowOncall); err != nil {
		t.Errorf("ValidateWindowKind(oncall) returned error: %v", err)
	}
	if err := ValidateWindowKind("vacation"); err == nil {
		t.Error("ValidateWindowKind(vacation) returned nil, want an error")
	}
	// Capacity is an enum and not a boolean, so all three are accepted.
	for _, capacity := range Capacities {
		if err := ValidateCapacity(capacity); err != nil {
			t.Errorf("ValidateCapacity(%s) returned error: %v", capacity, err)
		}
	}
	if err := ValidateCapacity("true"); err == nil {
		t.Error("ValidateCapacity(true) returned nil, want an error")
	}
}

func TestWindowRoundTrips(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	w := addCalendarEntry(t, st, WindowPTO, "2026-08-24", "2026-09-04", CapacityNone)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	loaded, err := tx.LoadCalendarWindow(ctx, w.ID)
	if err != nil {
		t.Fatalf("LoadCalendarWindow() returned error: %v", err)
	}
	if *loaded != *w {
		t.Errorf("loaded = %+v, want %+v", *loaded, *w)
	}
}

func TestListCalendarWindows(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	// newStore pins the clock to 2026-08-09.
	today := st.now().UTC().Format(DateFormat)
	if today != "2026-08-09" {
		t.Fatalf("test clock moved: today is %s", today)
	}

	past := addCalendarEntry(t, st, WindowPTO, "2026-07-01", "2026-07-10", CapacityNone)
	covering := addCalendarEntry(t, st, WindowOncall, "2026-08-03", "2026-08-16", CapacityReduced)
	future := addCalendarEntry(t, st, WindowHoliday, "2026-12-25", "2026-12-25", CapacityNone)

	tests := []struct {
		name   string
		filter WindowFilter
		want   []string
	}{
		{name: "all, earliest first", filter: WindowFilter{},
			want: []string{past.ID, covering.ID, future.ID}},
		{name: "current", filter: WindowFilter{Current: true}, want: []string{covering.ID}},
		{name: "upcoming keeps what has not ended", filter: WindowFilter{Upcoming: true},
			want: []string{covering.ID, future.ID}},
		{name: "on the last day", filter: WindowFilter{On: "2026-08-16"}, want: []string{covering.ID}},
		{name: "the day after", filter: WindowFilter{On: "2026-08-17"}, want: nil},
		{name: "by kind", filter: WindowFilter{Kind: WindowHoliday}, want: []string{future.ID}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := st.ListCalendarWindows(ctx, tt.filter)
			if err != nil {
				t.Fatalf("ListCalendarWindows() returned error: %v", err)
			}
			ids := make([]string, len(got))
			for i, w := range got {
				ids[i] = w.ID
			}
			if !equalStrings(ids, tt.want) {
				t.Errorf("ListCalendarWindows(%+v) = %v, want %v", tt.filter, ids, tt.want)
			}
		})
	}
}

// TestWindowIsAllAuthored checks nothing here is sync's: a window is
// hand-entered, so sync writing one would be meaningless.
func TestWindowIsAllAuthored(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	w := addCalendarEntry(t, st, WindowOncall, "2026-08-10", "2026-08-16", CapacityReduced)

	tx, err := st.Begin(ctx, ActorSyncGitHub)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	after := w.Clone()
	after.Capacity = CapacityFull
	if _, err := tx.Update(ctx, w, after); err == nil {
		t.Error("sync changed a calendar window, want an error")
	}
}

func TestLoadSubjectResolvesWindows(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	w := addCalendarEntry(t, st, WindowOncall, "2026-08-10", "2026-08-16", CapacityReduced)

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	subject, err := tx.LoadSubject(ctx, st, w.ID)
	if err != nil {
		t.Fatalf("LoadSubject() returned error: %v", err)
	}
	if subject.subjectType() != "calendar_window" || subject.subjectID() != w.ID {
		t.Errorf("LoadSubject() = %s/%s, want calendar_window/%s",
			subject.subjectType(), subject.subjectID(), w.ID)
	}
}

func TestSchemaRefusesAWindowThatEndsBeforeItStarts(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	w := NewCalendarWindow(WindowPTO, "2026-09-10")
	w.EndsOn = "2026-09-09"

	tx, err := st.Begin(ctx, ActorHuman)
	if err != nil {
		t.Fatalf("Begin() returned error: %v", err)
	}
	defer tx.Rollback()

	if err := tx.Insert(ctx, w); err == nil {
		t.Error("Insert() accepted a window ending before it starts, want the CHECK to refuse it")
	}
}
