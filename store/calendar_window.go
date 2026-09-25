package store

import "time"

// The run calendar's window, defined once so both dialects mean the same
// thing by it (#611).
//
// A request for N days is the N UTC calendar days ending today, today
// included. Previously SQLite read from midnight N days ago, which is
// N+1 days, and Postgres read a rolling N*24 hours, which starts partway
// through a day. Neither matched the other and neither matched N.

// MinCalendarDays and MaxCalendarDays bound what a caller may ask for.
const (
	MinCalendarDays = 1
	MaxCalendarDays = 365
)

// CalendarWindowStartTime returns midnight UTC on the first day of the
// window.
func CalendarWindowStartTime(days int) time.Time {
	if days < MinCalendarDays {
		days = MinCalendarDays
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return today.AddDate(0, 0, -(days - 1))
}

// CalendarWindowStart returns the same instant as an RFC 3339 string, for
// SQLite, where started_at is stored as text and compares lexically.
func CalendarWindowStart(days int) string {
	return CalendarWindowStartTime(days).Format(time.RFC3339)
}

// CalendarWindowDates lists every UTC day in the window, oldest first.
// The handler uses it to fill in days with no runs, so the response
// carries the window it covers rather than only the days that happen to
// have data.
func CalendarWindowDates(days int) []string {
	if days < MinCalendarDays {
		days = MinCalendarDays
	}
	start := CalendarWindowStartTime(days)
	out := make([]string, 0, days)
	for i := 0; i < days; i++ {
		out = append(out, start.AddDate(0, 0, i).Format("2006-01-02"))
	}
	return out
}
