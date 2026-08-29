package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// Backfill (ADR-028, #397 phase 3): create one run per schedule interval in
// a range, oldest first.
//
// A backfill is "re-do these slices", and everything here follows from the
// slices being first-class (phase 1): enumeration walks the pipeline's own
// schedule, each run carries its interval, and re-running an interval that
// was attempted before is deliberate -- the scheduled-dispatch unique index
// does not bind trigger "backfill", because runs are immutable history and
// a re-do appends rather than being refused.
//
// Dispatch is sequential -- concurrency 1 -- until pools exist (#398), and
// the plan says so rather than implying parallelism. Oldest first, so a
// consumer watching the destination sees time move forward.

// backfillMaxIntervals bounds one request. Deliberately a refusal rather
// than a truncation: a range that enumerates past the cap is refused
// naming the count, so the caller splits the range knowingly -- a silent
// cap would read as "covered everything" when it did not. A variable so
// tests can shrink it.
var backfillMaxIntervals = 500

// BackfillRequest is one backfill ask.
type BackfillRequest struct {
	Start, End time.Time
	// Force skips the interval-reference check. A backfill over a
	// pipeline that never mentions ${interval.*} creates N identical
	// runs, which is almost certainly a mistake -- but only almost, so
	// it is overridable rather than forbidden.
	Force bool
}

// BackfillPlan is what a accepted backfill will do, returned before the
// runs execute.
type BackfillPlan struct {
	PipelineID  string    `json:"pipeline_id"`
	Intervals   int       `json:"intervals"`
	First       time.Time `json:"first_interval_start"`
	Last        time.Time `json:"last_interval_end"`
	Concurrency int       `json:"concurrency"`
	Note        string    `json:"note"`
}

// Backfill validates the ask, enumerates the intervals, and starts the
// sequential dispatch in the background. The returned plan describes what
// was started; the runs themselves appear in run history as they execute,
// trigger "backfill", oldest first.
func (e *Engine) Backfill(pipelineID string, req BackfillRequest) (*BackfillPlan, error) {
	if e.closing() {
		return nil, ErrEngineClosed
	}
	if !req.End.After(req.Start) {
		return nil, fmt.Errorf("backfill range is empty: end %s is not after start %s",
			req.End.Format(time.RFC3339), req.Start.Format(time.RFC3339))
	}
	pipe, err := e.store.GetPipeline(pipelineID)
	if err != nil {
		return nil, fmt.Errorf("get pipeline: %w", err)
	}
	if pipe.Schedule == "" {
		return nil, fmt.Errorf(
			"pipeline %q has no schedule, so there is no interval grid to backfill over; "+
				"give it a schedule first, or run it manually with explicit params", pipe.Name)
	}
	sched, err := scheduleFor(pipe.Schedule, pipe.ScheduleTimezone)
	if err != nil {
		return nil, fmt.Errorf("parse schedule: %w", err)
	}
	if !req.Force && !pipelineReferencesInterval(pipe) {
		return nil, fmt.Errorf(
			"pipeline %q never references ${interval.start}, ${interval.end} or ${param.date}, so "+
				"every backfill run would do identical work -- the runs would not be scoped to their "+
				"slices. Add the interval to a query or path, or pass force to backfill anyway", pipe.Name)
	}

	intervals, err := enumerateIntervals(sched, req.Start, req.End)
	if err != nil {
		return nil, err
	}
	if len(intervals) == 0 {
		return nil, fmt.Errorf(
			"the schedule has no complete interval inside %s .. %s; nothing to backfill",
			req.Start.Format(time.RFC3339), req.End.Format(time.RFC3339))
	}

	plan := &BackfillPlan{
		PipelineID:  pipelineID,
		Intervals:   len(intervals),
		First:       intervals[0][0],
		Last:        intervals[len(intervals)-1][1],
		Concurrency: 1,
		Note:        "runs dispatch sequentially, oldest interval first; concurrency rises when pools (#398) exist",
	}

	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		failed := 0
		for i, iv := range intervals {
			if e.closing() {
				common.SLog().Warn("backfill stopped by shutdown",
					common.PipelineAttr(pipelineID), "dispatched", i, "of", len(intervals))
				return
			}
			start, end := iv[0], iv[1]
			run, err := e.RunPipelineOpts(pipelineID, RunOptions{
				Trigger:           models.RunTriggerBackfill,
				DataIntervalStart: &start,
				DataIntervalEnd:   &end,
				// The date param the pre-ADR-028 backfill injected, kept
				// so pipelines built on ${param.date} backfill unchanged.
				// The interval variables are the real interface; this is
				// the grandfather clause, set to the slice's start date.
				Params: map[string]string{"date": start.Format("2006-01-02")},
			})
			// A failed slice does not stop the rest: each interval is its
			// own unit of work, later slices do not depend on an earlier
			// one succeeding, and the per-run failure is already recorded
			// where run failures live. The summary below counts them.
			if err != nil {
				failed++
				common.SLog().Error("backfill run failed to dispatch",
					common.PipelineAttr(pipelineID), "interval_start", start, "error", err)
			} else if run != nil && run.Status == models.RunStatusFailed {
				failed++
			}
		}
		common.SLog().Info("backfill complete",
			common.PipelineAttr(pipelineID), "intervals", len(intervals), "failed", failed)
	}()

	return plan, nil
}

// enumerateIntervals lists the schedule's complete intervals inside
// [start, end]: consecutive tick pairs [a, b) with a >= start and
// b <= end, oldest first.
func enumerateIntervals(sched interface {
	Next(time.Time) time.Time
}, start, end time.Time) ([][2]time.Time, error) {
	var out [][2]time.Time
	a := sched.Next(start.Add(-time.Nanosecond))
	for !a.IsZero() && !a.After(end) {
		b := sched.Next(a)
		if b.IsZero() || b.After(end) {
			break
		}
		out = append(out, [2]time.Time{a, b})
		if len(out) > backfillMaxIntervals {
			return nil, fmt.Errorf(
				"the range enumerates more than %d intervals; split the backfill into narrower "+
					"ranges -- refusing outright beats silently truncating what \"backfilled\" means",
				backfillMaxIntervals)
		}
		a = b
	}
	return out, nil
}

// pipelineReferencesInterval reports whether any node config string
// mentions ${interval. -- the check behind the not-scoped-to-its-slice
// refusal, and the phase-3 landing spot for the validation warning phase 1
// deferred.
func pipelineReferencesInterval(p *models.Pipeline) bool {
	var walk func(v interface{}) bool
	walk = func(v interface{}) bool {
		switch t := v.(type) {
		case string:
			// ${param.date} is the pre-ADR-028 backfill convention,
			// grandfathered: those pipelines ARE slice-scoped, by the
			// param the dispatch loop still injects.
			return strings.Contains(t, "${interval.") || strings.Contains(t, "${param.date}")
		case map[string]interface{}:
			for _, vv := range t {
				if walk(vv) {
					return true
				}
			}
		case []interface{}:
			for _, vv := range t {
				if walk(vv) {
					return true
				}
			}
		}
		return false
	}
	for _, n := range p.Nodes {
		if walk(map[string]interface{}(n.Config)) {
			return true
		}
	}
	return false
}
