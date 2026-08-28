package engine

import (
	"testing"
	"time"
)

// The DST/previous-tick table (ADR-028's named cost), written before the
// scheduler stamping that consumes it. Every expectation here is a literal
// -- no case is asserted by round-tripping through the code under test.
func TestPrevTickTable(t *testing.T) {
	mustTime := func(layout, s string, loc *time.Location) time.Time {
		t.Helper()
		tm, err := time.ParseInLocation(layout, s, loc)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	utc := time.UTC
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		expr     string
		timezone string
		at       time.Time
		want     time.Time
	}{
		{
			name: "hourly, plain UTC",
			expr: "0 * * * *", at: mustTime("2006-01-02 15:04", "2026-08-28 14:00", utc),
			want: mustTime("2006-01-02 15:04", "2026-08-28 13:00", utc),
		},
		{
			name: "daily at 06:00",
			expr: "0 6 * * *", at: mustTime("2006-01-02 15:04", "2026-08-28 06:00", utc),
			want: mustTime("2006-01-02 15:04", "2026-08-27 06:00", utc),
		},
		{
			name: "every five minutes",
			expr: "*/5 * * * *", at: mustTime("2006-01-02 15:04", "2026-08-28 12:35", utc),
			want: mustTime("2006-01-02 15:04", "2026-08-28 12:30", utc),
		},
		{
			name: "weekly (monday 03:00), a week apart",
			expr: "0 3 * * 1", at: mustTime("2006-01-02 15:04", "2026-08-24 03:00", utc), // a Monday
			want: mustTime("2006-01-02 15:04", "2026-08-17 03:00", utc),
		},
		{
			name: "monthly on the 1st, across a month boundary",
			expr: "0 0 1 * *", at: mustTime("2006-01-02 15:04", "2026-09-01 00:00", utc),
			want: mustTime("2006-01-02 15:04", "2026-08-01 00:00", utc),
		},
		{
			name: "t not on a tick: last tick strictly before t",
			expr: "0 * * * *", at: mustTime("2006-01-02 15:04", "2026-08-28 14:30", utc),
			want: mustTime("2006-01-02 15:04", "2026-08-28 14:00", utc),
		},
		{
			// Spring forward, America/New_York 2026: 02:00 EST jumps to
			// 03:00 EDT on March 8th. A daily 01:30 schedule fires both
			// days; the interval endpoints are 23 real hours apart, and
			// the derivation must report the actual previous activation,
			// not "24 hours earlier".
			name: "daily 01:30 across spring-forward",
			expr: "30 1 * * *", timezone: "America/New_York",
			at:   mustTime("2006-01-02 15:04", "2026-03-08 01:30", ny),
			want: mustTime("2006-01-02 15:04", "2026-03-07 01:30", ny),
		},
		{
			// The skipped hour: 02:30 does not exist on March 8th 2026
			// (02:00 jumps to 03:00), and the library's measured answer
			// is to SKIP the day -- no March 8th activation at all. The
			// previous tick before March 9th's run is therefore March
			// 7th's, and the interval [Mar 7 02:30, Mar 9 02:30) spans
			// two days. That is the operationally honest outcome: the
			// skipped day's data is not lost, it lands in the next run's
			// wider interval, and the interval chain stays gapless.
			name: "daily 02:30: spring-forward skips the day, the interval widens",
			expr: "30 2 * * *", timezone: "America/New_York",
			at:   mustTime("2006-01-02 15:04", "2026-03-09 02:30", ny),
			want: time.Date(2026, 3, 7, 2, 30, 0, 0, ny),
		},
		{
			// The repeated hour: 01:30 occurs twice on November 1st 2026
			// (EDT then EST), and the library's measured answer is to
			// fire BOTH. The previous tick before November 2nd's run is
			// the second occurrence (01:30 EST), giving that doubled day
			// two adjacent intervals -- the second only one real hour
			// long -- and again no gap and no overlap in the chain.
			name: "daily 01:30: fall-back fires twice, two adjacent intervals",
			expr: "30 1 * * *", timezone: "America/New_York",
			at:   mustTime("2006-01-02 15:04", "2026-11-02 01:30", ny),
			want: time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), // 01:30 EST, the second firing
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sched, err := scheduleFor(tc.expr, tc.timezone)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := prevTick(sched, tc.at)
			if !ok {
				t.Fatalf("no previous tick found before %s", tc.at)
			}
			if !got.Equal(tc.want) {
				t.Errorf("prevTick(%q tz=%q, %s) = %s, want %s",
					tc.expr, tc.timezone, tc.at, got, tc.want)
			}
			// The contract the interval leans on: the previous tick is
			// strictly before t, and the schedule fires nothing between
			// the two -- [prev, t) really is one whole interval.
			if !got.Before(tc.at) {
				t.Error("previous tick is not strictly before t")
			}
			if n := sched.Next(got); n.Before(tc.at) {
				t.Errorf("a tick exists between prev and t (%s); the interval is not whole", n)
			}
		})
	}
}

// A schedule with no activation inside the lookback bound says so instead
// of walking forever.
func TestPrevTickBounded(t *testing.T) {
	// February 30th never occurs.
	sched, err := scheduleFor("0 0 30 2 *", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := prevTick(sched, time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)); ok {
		t.Fatal("an impossible schedule must report no previous tick, not hang")
	}
}
