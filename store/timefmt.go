package store

import (
	"fmt"
	"strings"
	"time"
)

// timeFormat is how SQLite stores a timestamp: UTC, nanoseconds always
// written out. SQLite has no time type, so the store compares and orders
// timestamps as text, and that is only correct when every value has the
// same width. time.RFC3339Nano trims trailing zeros, so "…05.1Z" and
// "…05.123Z" sort the wrong way round and "…05Z" sorts after both
// ('Z' is above '.'). Write timestamps with formatTime, never with
// Format(time.RFC3339Nano) or by handing a time.Time to the driver, which
// stores time.Time.String().
const timeFormat = "2006-01-02T15:04:05.000000000Z07:00"

// formatTime renders t for a SQLite timestamp column.
func formatTime(t time.Time) string {
	return t.UTC().Format(timeFormat)
}

// legacyTimeLayouts are the shapes older versions wrote into SQLite:
// RFC3339Nano (trimmed fraction, any offset), time.Time.String() from a
// time.Time handed to the driver, and SQLite's own datetime('now').
// Values without an offset are UTC.
var legacyTimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
}

// parseTime reads a SQLite timestamp column. It accepts what formatTime
// writes and every legacy shape, so a value that predates migration 2 (or
// was written by code outside the store) still reads back.
func parseTime(s string) (time.Time, error) {
	// time.Time.String() appends a monotonic clock reading: " m=+1.234".
	if i := strings.Index(s, " m="); i > 0 {
		s = s[:i]
	}
	for _, layout := range legacyTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q", s)
}
