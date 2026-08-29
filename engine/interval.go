package engine

import (
	"time"

	"github.com/robfig/cron/v3"
)

// Data-interval derivation (ADR-028, #397 phase 1).
//
// A scheduled run fired at tick T is responsible for the half-open interval
// [previous tick, T): data for a window is complete when the window closes,
// so the run fires at its interval's end. The cron library answers "next
// activation after t" and nothing else, so the previous tick is derived
// from Next by walking.
//
// The derivation must hold under timezones and DST, which is why its test
// table exists before its first caller (the ADR names this as the fiddly
// center): the scheduler wraps zoned schedules so Next already thinks in
// the schedule's location, and building prevTick purely on Next means it
// inherits that behaviour instead of re-implementing it wrongly.

// maxIntervalLookback bounds the search for a previous tick. A schedule
// whose ticks are more than a year apart has no meaningful data interval
// for our purposes, and an unbounded walk on a pathological schedule (one
// that never fires) must terminate.
const maxIntervalLookback = 366 * 24 * time.Hour

// prevTick returns the last activation of sched strictly before t, and
// whether one exists within the lookback bound.
//
// t is expected to be an activation time itself (the tick the scheduler is
// firing for), but the derivation does not depend on that: it returns the
// last tick < t for any t.
func prevTick(sched cron.Schedule, t time.Time) (time.Time, bool) {
	// Walk back exponentially until the schedule's next activation from
	// the seed lands strictly before t -- i.e. the seed is far enough back
	// that at least one tick lies between it and t.
	// The library reports "never fires again" as the zero time, which is
	// Before() everything -- treated naively it walks this function into
	// an infinite loop, which is exactly how the bounded-schedule test
	// caught it. A zero Next anywhere means no tick exists in the window
	// being examined.
	back := time.Minute
	seed := t.Add(-back)
	for {
		n := sched.Next(seed)
		if n.IsZero() {
			// Nothing fires after seed at all (the library scans years
			// ahead); with seed inside the lookback of t, there is no
			// previous tick to find.
			return time.Time{}, false
		}
		if n.Before(t) {
			break
		}
		back *= 2
		if back > maxIntervalLookback {
			return time.Time{}, false
		}
		seed = t.Add(-back)
	}
	// Walk forward from the seed to the last tick before t. The step count
	// is bounded: the exponential phase guarantees the seed is within
	// 2*gap of t for a schedule with gap-spaced ticks, so this loop runs a
	// small constant number of times for real schedules and at most
	// (lookback / smallest tick spacing) for adversarial ones -- cron's
	// finest resolution is one minute, and the walk-back doubling means
	// the seed is at most twice the true gap away.
	prev := sched.Next(seed)
	for {
		n := sched.Next(prev)
		if n.IsZero() || !n.Before(t) {
			return prev, true
		}
		prev = n
	}
}

// scheduleFor parses a pipeline's cron expression the way the scheduler
// does -- same parser options, same timezone wrapping -- so interval
// derivation and dispatch cannot disagree about what a schedule means.
func scheduleFor(expr, timezone string) (cron.Schedule, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(expr)
	if err != nil {
		return nil, err
	}
	if timezone != "" {
		if loc, lerr := time.LoadLocation(timezone); lerr == nil {
			return &tzCronSchedule{inner: sched, loc: loc}, nil
		}
	}
	return sched, nil
}
