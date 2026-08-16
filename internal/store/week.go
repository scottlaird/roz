package store

import (
	"fmt"
	"strings"
	"time"
)

// Days, as they are stored: names rather than numbers, because the log reads
// and nothing here does arithmetic on them that a name prevents.
var weekdayNames = map[string]time.Weekday{
	"sunday":    time.Sunday,
	"monday":    time.Monday,
	"tuesday":   time.Tuesday,
	"wednesday": time.Wednesday,
	"thursday":  time.Thursday,
	"friday":    time.Friday,
	"saturday":  time.Saturday,
}

// WeekdayNames are the accepted values, in the order a week is usually written
// rather than in Go's, which starts on Sunday.
var WeekdayNames = []string{
	"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday",
}

// ValidateWeekday refuses a day the schema would refuse, naming the set. It is
// closed and cannot grow, unlike the tracker vocabularies.
func ValidateWeekday(setting, day string) error {
	if _, ok := weekdayNames[strings.ToLower(strings.TrimSpace(day))]; ok {
		return nil
	}
	return fmt.Errorf("%s %q is not a day: use one of %s",
		setting, day, strings.Join(WeekdayNames, ", "))
}

// Week is one reporting period: what it covers, and what it is called.
type Week struct {
	// Start and End are the days the window covers, both inclusive. A review
	// run at five on a Friday afternoon has to cover that Friday's merges, so
	// the window includes the review day rather than stopping at its midnight
	// — "since Friday" read as midnight silently drops the day being reviewed.
	Start time.Time
	End   time.Time
	// Label is the day the week is named from, which is a week_start and may
	// fall inside the window rather than at either end of it.
	Label time.Time
}

// Covers reports whether an instant falls in the window. Half-open at the top:
// End is a date, and everything up to the following midnight belongs to it.
func (w Week) Covers(at time.Time) bool {
	at = at.UTC()
	return !at.Before(w.Start) && at.Before(w.End.AddDate(0, 0, 1))
}

// Title is the heading a report carries.
func (w Week) Title() string { return "Week of " + w.Label.Format("January 2") }

// WeekOf works out the reporting period containing an instant.
//
// # The window
//
// Seven days ending at the most recent review_day, that day included. Every
// day then belongs to exactly one window: the previous review covered up to
// its own day, and the next one starts where this left off. Where the instant
// falls on the review day itself, that day ends the window rather than
// starting the next one — a review is a look backwards, and running one should
// report the day it is run on.
//
// # The label
//
// The week_start of whichever week most of the window falls in.
//
// One rule rather than two, and it settles the case the issue said had to be
// chosen. With week_start = monday and review_day = friday, the window is Sat
// to Fri and five of its seven days are in the week beginning that Monday, so
// the report is headed with it — which is what anybody would write by hand.
// The window then begins two days *before* the date in its own heading, and
// that is right rather than a bug: the previous review was the Friday before,
// so the weekend after it has not been reported yet and belongs here.
//
// Where the two settings coincide — review on a Monday, weeks starting Monday
// — the window is Tue to Mon and six of its seven days are in the *earlier*
// week, so that is the label. Reading it the other way would head a report with
// a week it contains one day of. This is the one combination where the answer
// is not obvious, and majority is what makes it fall out of the same rule as
// every other.
func WeekOf(at time.Time, weekStart, reviewDay string) (Week, error) {
	start, ok := weekdayNames[strings.ToLower(weekStart)]
	if !ok {
		return Week{}, fmt.Errorf("week_start %q is not a day", weekStart)
	}
	review, ok := weekdayNames[strings.ToLower(reviewDay)]
	if !ok {
		return Week{}, fmt.Errorf("review_day %q is not a day", reviewDay)
	}

	// The day itself, with the time thrown away: a window is a span of days.
	day := time.Date(at.UTC().Year(), at.UTC().Month(), at.UTC().Day(), 0, 0, 0, 0, time.UTC)

	// Back to the most recent review day, which is today when today is one.
	end := day.AddDate(0, 0, -daysBack(day.Weekday(), review))
	begin := end.AddDate(0, 0, -6)

	return Week{Start: begin, End: end, Label: labelFor(begin, start)}, nil
}

// daysBack is how far back the most recent given weekday is, counting today as
// nought rather than as seven.
func daysBack(from, to time.Weekday) int {
	back := int(from-to+7) % 7
	return back
}

// labelFor picks the week the window mostly belongs to.
//
// Counting rather than comparing: the window is seven days, so whichever
// week_start falls inside it splits the window in two, and the larger part is
// the answer. A window whose first day is a week_start is wholly inside one
// week and takes it.
func labelFor(begin time.Time, start time.Weekday) time.Time {
	// The week_start on or before the window opens.
	first := begin.AddDate(0, 0, -daysBack(begin.Weekday(), start))
	// The next one, which may or may not fall inside the window.
	next := first.AddDate(0, 0, 7)

	inFirst := int(next.Sub(begin).Hours() / 24)
	if inFirst >= 7 {
		return first // the window opens on a week_start
	}
	if inFirst >= 7-inFirst {
		return first
	}
	return next
}
