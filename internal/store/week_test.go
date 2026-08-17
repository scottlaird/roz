package store

import (
	"strings"
	"testing"
	"time"
)

func day(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatalf("parsing %q: %v", s, err)
	}
	return parsed
}

// TestTheWorkedExample is #224's, verbatim. week_start = monday, review_day =
// friday, review run on Friday 14 August 2026: headed "Week of August 10" and
// covering Saturday 8 through Friday 14.
//
// The window begins two days before the date in its own heading, which is the
// part worth pinning: the previous review was Friday 7, so the weekend after
// it has not been reported yet and belongs here.
func TestTheWorkedExample(t *testing.T) {
	week, err := WeekOf(day(t, "2026-08-14"), "monday", "friday")
	if err != nil {
		t.Fatalf("WeekOf() returned error: %v", err)
	}
	if got := week.Start.Format("2006-01-02"); got != "2026-08-08" {
		t.Errorf("start = %s, want 2026-08-08 (Saturday)", got)
	}
	if got := week.End.Format("2006-01-02"); got != "2026-08-14" {
		t.Errorf("end = %s, want 2026-08-14 (Friday)", got)
	}
	if got := week.Title(); got != "Week of August 10" {
		t.Errorf("title = %q, want %q", got, "Week of August 10")
	}
	// Exactly seven days, so nothing is reported twice and nothing is missed.
	if days := int(week.End.Sub(week.Start).Hours()/24) + 1; days != 7 {
		t.Errorf("window is %d days, want 7", days)
	}
}

// TestTheReviewDayIsInsideItsOwnWindow. A review run at five on a Friday
// afternoon has to cover that Friday's merges; "since Friday" read as midnight
// silently drops the day being reviewed.
func TestTheReviewDayIsInsideItsOwnWindow(t *testing.T) {
	week, err := WeekOf(day(t, "2026-08-14"), "monday", "friday")
	if err != nil {
		t.Fatalf("WeekOf() returned error: %v", err)
	}
	for _, at := range []string{
		"2026-08-08T00:00:00Z", // the first moment of the window
		"2026-08-14T17:00:00Z", // five o'clock on the review day
		"2026-08-14T23:59:59Z", // and the last moment of it
	} {
		when, _ := time.Parse(time.RFC3339, at)
		if !week.Covers(when) {
			t.Errorf("the window does not cover %s", at)
		}
	}
	for _, at := range []string{
		"2026-08-07T23:59:59Z", // the previous review's day
		"2026-08-15T00:00:00Z", // the Saturday after, which is next week's
	} {
		when, _ := time.Parse(time.RFC3339, at)
		if week.Covers(when) {
			t.Errorf("the window covers %s, which belongs to another one", at)
		}
	}
}

// TestEveryDayBelongsToExactlyOneWindow is the property the whole thing is
// for: run the review on every day of a year and no day is reported twice or
// missed.
func TestEveryDayBelongsToExactlyOneWindow(t *testing.T) {
	// Read from the last review of the year backwards, checking each window
	// abuts the one before it.
	var previous Week
	for d := day(t, "2026-01-08"); d.Before(day(t, "2027-01-01")); d = d.AddDate(0, 0, 1) {
		week, err := WeekOf(d, "monday", "friday")
		if err != nil {
			t.Fatalf("WeekOf(%s) returned error: %v", d, err)
		}
		if previous.End.IsZero() || week.End.Equal(previous.End) {
			previous = week
			continue
		}
		// A new window opens the day after the last one closed.
		if want := previous.End.AddDate(0, 0, 1); !week.Start.Equal(want) {
			t.Fatalf("the window ending %s starts %s, want %s: days are dropped or repeated",
				week.End.Format("2006-01-02"), week.Start.Format("2006-01-02"),
				want.Format("2006-01-02"))
		}
		previous = week
	}
}

// TestTheLabelIsTheWeekMostOfTheWindowIsIn, including the case #224 says has
// to be chosen: when the two settings coincide the window is Tue to Mon, and
// six of its seven days are in the *earlier* week. Labelling it with the later
// one would head a report with a week it contains one day of.
func TestTheLabelIsTheWeekMostOfTheWindowIsIn(t *testing.T) {
	tests := []struct {
		name              string
		on, start, review string
		want              string
	}{
		{"monday weeks, friday review", "2026-08-14", "monday", "friday", "Week of August 10"},
		{"sunday weeks, friday review", "2026-08-14", "sunday", "friday", "Week of August 9"},
		{"the settings coincide", "2026-08-10", "monday", "monday", "Week of August 3"},
		{"review the day before the week turns", "2026-08-09", "monday", "sunday", "Week of August 3"},
		{"review the day the week turns", "2026-08-10", "monday", "monday", "Week of August 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			week, err := WeekOf(day(t, tt.on), tt.start, tt.review)
			if err != nil {
				t.Fatalf("WeekOf() returned error: %v", err)
			}
			if got := week.Title(); got != tt.want {
				t.Errorf("title = %q, want %q (window %s to %s)", got, tt.want,
					week.Start.Format("Mon 2 Jan"), week.End.Format("Mon 2 Jan"))
			}
		})
	}
}

// TestTheWindowEndsAtTheLastReview, not at the next one: asked on a Wednesday,
// the answer is the week that ended last Friday rather than a week that has
// not happened.
func TestTheWindowEndsAtTheLastReview(t *testing.T) {
	week, err := WeekOf(day(t, "2026-08-19"), "monday", "friday") // a Wednesday
	if err != nil {
		t.Fatalf("WeekOf() returned error: %v", err)
	}
	if got := week.End.Format("2006-01-02"); got != "2026-08-14" {
		t.Errorf("end = %s, want the Friday just gone", got)
	}
}

// TestADayThatIsNotOneIsRefused, naming the set rather than quoting a CHECK.
func TestADayThatIsNotOneIsRefused(t *testing.T) {
	if _, err := WeekOf(time.Now(), "monday", "someday"); err == nil {
		t.Error("WeekOf() accepted a review day that is not a day")
	}
	if err := ValidateWeekday("--review-day", "friday"); err != nil {
		t.Errorf("ValidateWeekday(friday) returned error: %v", err)
	}
	err := ValidateWeekday("--review-day", "caturday")
	if err == nil {
		t.Fatal("ValidateWeekday accepted caturday")
	}
	if !strings.Contains(err.Error(), "monday") {
		t.Errorf("error = %v, want it to name the days", err)
	}
}
