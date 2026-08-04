package engine

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/robfig/cron/v3"
)

// ScheduleInfo describes an active schedule for API responses.
type ScheduleInfo struct {
	PipelineID   string    `json:"pipeline_id"`
	PipelineName string    `json:"pipeline_name"`
	Schedule     string    `json:"schedule"`
	NextRun      time.Time `json:"next_run"`
	LastRun      string    `json:"last_run,omitempty"` // ISO timestamp or empty
}

// Scheduler manages cron-based pipeline scheduling.
type Scheduler struct {
	cron      *cron.Cron
	engine    *Engine
	store     store.Store
	entries   map[string]cron.EntryID // pipelineID -> cron entry
	schedules map[string]string       // pipelineID -> cron expression
	names     map[string]string       // pipelineID -> pipeline name
	mu        sync.Mutex

	// leader gates actual dispatch (Tnsor-Labs/brokoli#10): every instance
	// still loads pipelines and holds cron entries ready (so Status/NextRun
	// keep working identically on every instance — see point 5 of the
	// issue, "read-only endpoints on any instance"), but only the current
	// leader's cron ticks and catch-up pass are allowed to actually call
	// engine.RunPipeline. Defaults to store.NewNoopLeaderElector() (always
	// leader) so single-instance/SQLite deployments and existing callers
	// that don't know about leader election see zero behavior change.
	leader store.LeaderElector
}

// NewScheduler creates a scheduler that uses the given engine to run
// pipelines. leader gates whether this instance's cron ticks and missed-run
// catch-up actually dispatch (Tnsor-Labs/brokoli#10); pass nil to always
// dispatch (single-instance/SQLite behavior, and every prior caller's
// implicit expectation before leader election existed).
func NewScheduler(engine *Engine, s store.Store, leader store.LeaderElector) *Scheduler {
	if leader == nil {
		leader = store.NewNoopLeaderElector()
	}
	return &Scheduler{
		cron:      cron.New(cron.WithLocation(time.UTC)),
		engine:    engine,
		store:     s,
		entries:   make(map[string]cron.EntryID),
		schedules: make(map[string]string),
		names:     make(map[string]string),
		leader:    leader,
	}
}

// tzCronSchedule wraps a cron.Schedule with per-pipeline timezone support.
type tzCronSchedule struct {
	inner cron.Schedule
	loc   *time.Location
}

func (s *tzCronSchedule) Next(t time.Time) time.Time {
	// Convert to pipeline's timezone, compute next fire time, convert back to UTC
	tInTZ := t.In(s.loc)
	nextInTZ := s.inner.Next(tInTZ)
	return nextInTZ.UTC()
}

// Start loads all scheduled pipelines, checks for missed runs, and begins the cron scheduler.
func (s *Scheduler) Start() error {
	pipelines, err := s.store.ListPipelines()
	if err != nil {
		return fmt.Errorf("list pipelines: %w", err)
	}

	registered := 0
	for _, p := range pipelines {
		if p.Enabled && p.Schedule != "" {
			if err := s.Register(p.ID, p.Name, p.Schedule, p.ScheduleTimezone); err != nil {
				log.Printf("WARNING: failed to register schedule for pipeline %s (%s): %v", p.Name, p.Schedule, err)
			} else {
				registered++
			}
		}
	}

	// Run catch-up BEFORE starting cron to prevent duplicate runs
	s.catchUpMissedRuns(pipelines)

	s.cron.Start()
	common.SLog().Info("scheduler started", "pipelines_scheduled", registered)

	return nil
}

// catchUpMissedRuns checks each scheduled pipeline's last run vs its schedule.
// If a run was missed during downtime, triggers it now.
//
// Gated on leadership (Tnsor-Labs/brokoli#10): a standby instance must not
// independently fire the same missed-run dispatch a concurrently-booting
// leader (or the leader that never went down at all) already owns. Checked
// both up front (cheap early exit for the common non-leader case) and
// again on every loop iteration below, since a long pipeline list could in
// principle take long enough for this instance's lease to lapse mid-pass.
func (s *Scheduler) catchUpMissedRuns(pipelines []models.Pipeline) {
	if !s.leader.IsLeader() {
		common.SLog().Debug("catch-up skipped: not leader")
		return
	}
	for _, p := range pipelines {
		if !p.Enabled || p.Schedule == "" {
			continue
		}
		// Re-check leadership on every iteration, not just once up front:
		// a long pipeline list could take long enough for this instance's
		// lease to lapse and a new leader to be elected mid-loop, and we
		// must stop dispatching the moment that happens rather than
		// racing the new leader's own catch-up pass over the same
		// pipelines still left in this loop.
		if !s.leader.IsLeader() {
			common.SLog().Info("catch-up stopped mid-pass: leadership lost")
			return
		}

		// Parse the cron schedule
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		sched, err := parser.Parse(p.Schedule)
		if err != nil {
			continue
		}

		// Wrap with timezone if configured
		var schedule cron.Schedule = sched
		if p.ScheduleTimezone != "" {
			if loc, lerr := time.LoadLocation(p.ScheduleTimezone); lerr == nil {
				schedule = &tzCronSchedule{inner: sched, loc: loc}
			}
		}

		// Get the last run for this pipeline
		runs, err := s.store.ListRunsByPipeline(p.ID, 1)
		if err != nil || len(runs) == 0 {
			continue // no runs yet, don't catch up on first deploy
		}

		now := time.Now().UTC()

		// Get last run time (may be a pointer)
		var lastRunTime time.Time
		if runs[0].StartedAt != nil {
			lastRunTime = *runs[0].StartedAt
		} else {
			continue
		}

		// Find when the schedule should have fired after the last run
		nextExpected := schedule.Next(lastRunTime)

		// If the expected fire time is in the past (we missed it), and it's been less than 24h
		if nextExpected.Before(now) && now.Sub(nextExpected) < 24*time.Hour {
			common.SLog().Info("catch-up: pipeline missed scheduled run, triggering now",
				common.PipelineAttr(p.ID), "pipeline_name", p.Name, "expected_at", nextExpected)
			go func(pid string) {
				if _, err := s.engine.RunPipeline(pid); err != nil {
					common.SLog().Error("catch-up run failed", common.PipelineAttr(pid), "error", err)
				}
			}(p.ID)
		}
	}
}

// Stop gracefully stops the scheduler.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done() // wait for running jobs to finish
}

// Register adds or updates a cron schedule for a pipeline.
// scheduleTimezone is optional — if empty, UTC is used.
func (s *Scheduler) Register(pipelineID, pipelineName, schedule, scheduleTimezone string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove existing entry if any
	if entryID, ok := s.entries[pipelineID]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, pipelineID)
		delete(s.schedules, pipelineID)
		delete(s.names, pipelineID)
	}

	// Parse timezone
	loc := time.UTC
	if scheduleTimezone != "" {
		parsed, err := time.LoadLocation(scheduleTimezone)
		if err == nil {
			loc = parsed
		} else {
			log.Printf("WARNING: invalid schedule timezone %q for pipeline %s, falling back to UTC", scheduleTimezone, pipelineName)
		}
	}

	// Parse cron expression and wrap with timezone
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	cronSchedule, err := parser.Parse(schedule)
	if err != nil {
		return fmt.Errorf("parse cron: %w", err)
	}
	var tzSched cron.Schedule = cronSchedule
	if loc != time.UTC {
		tzSched = &tzCronSchedule{inner: cronSchedule, loc: loc}
	}

	pid := pipelineID // capture for closure
	entryID := s.cron.Schedule(tzSched, cron.FuncJob(func() {
		// Leadership gate (Tnsor-Labs/brokoli#10): every instance still
		// registers this cron entry (so Status()/NextRun() report the same
		// schedule everywhere — see catchUpMissedRuns' doc comment above
		// for why standbys still need to hold entries ready), but only the
		// current leader is allowed to actually dispatch. Standbys log and
		// skip on every tick they're not leader, which is a normal,
		// expected, non-error condition — not logged as a warning.
		if !s.leader.IsLeader() {
			common.SLog().Debug("scheduled fire skipped: not leader", common.PipelineAttr(pid))
			return
		}
		common.SLog().Info("scheduled run triggered", common.PipelineAttr(pid))
		run, err := s.engine.RunPipeline(pid)
		if err != nil {
			common.SLog().Error("scheduled run failed", common.PipelineAttr(pid), "error", err)
			return
		}
		if run != nil && run.Status == models.RunStatusBlocked {
			common.SLog().Warn("scheduled run blocked", common.PipelineAttr(pid), common.RunAttr(run.ID), "reason", run.Error)
		} else if run != nil {
			common.SLog().Info("scheduled run dispatched", common.PipelineAttr(pid), common.RunAttr(run.ID))
		}
	}))

	s.entries[pipelineID] = entryID
	s.schedules[pipelineID] = schedule
	s.names[pipelineID] = pipelineName
	return nil
}

// Unregister removes the cron schedule for a pipeline.
func (s *Scheduler) Unregister(pipelineID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entryID, ok := s.entries[pipelineID]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, pipelineID)
		delete(s.schedules, pipelineID)
		delete(s.names, pipelineID)
	}
}

// SyncPipeline updates the scheduler when a pipeline is saved.
// Call this after every pipeline update.
func (s *Scheduler) SyncPipeline(pipelineID, pipelineName, schedule string, enabled bool, scheduleTimezone string) {
	if enabled && schedule != "" {
		if err := s.Register(pipelineID, pipelineName, schedule, scheduleTimezone); err != nil {
			log.Printf("WARNING: failed to sync schedule for %s: %v", pipelineName, err)
		}
	} else {
		s.Unregister(pipelineID)
	}
}

// Status returns info about all active schedules.
func (s *Scheduler) Status() []ScheduleInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	var infos []ScheduleInfo
	for pid, entryID := range s.entries {
		entry := s.cron.Entry(entryID)
		info := ScheduleInfo{
			PipelineID:   pid,
			PipelineName: s.names[pid],
			Schedule:     s.schedules[pid],
			NextRun:      entry.Next,
		}

		// Get last run time
		runs, err := s.store.ListRunsByPipeline(pid, 1)
		if err == nil && len(runs) > 0 && runs[0].StartedAt != nil {
			info.LastRun = runs[0].StartedAt.UTC().Format(time.RFC3339)
		}

		infos = append(infos, info)
	}
	return infos
}

// NextRun returns the next scheduled run time for a pipeline, or zero time if not scheduled.
func (s *Scheduler) NextRun(pipelineID string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entryID, ok := s.entries[pipelineID]; ok {
		return s.cron.Entry(entryID).Next
	}
	return time.Time{}
}

// IsLeader reports whether this instance currently believes it is the
// scheduler leader — always true for the default NoopLeaderElector
// (single-instance/SQLite). Read-only callers (e.g. status endpoints) don't
// need this; it exists for api.PrometheusHandler and diagnostics, mirroring
// how engine.Engine exposes RunsTotal/RunsSucceeded/RunsFailed directly for
// that same handler to read at scrape time (Tnsor-Labs/brokoli#10 point 6).
func (s *Scheduler) IsLeader() bool { return s.leader.IsLeader() }

// LeaderFencingGeneration returns the fencing generation for this
// instance's current leadership tenure, 0 whenever IsLeader() is false —
// see store.LeaderElector.FencingGeneration for the full contract.
func (s *Scheduler) LeaderFencingGeneration() int64 { return s.leader.FencingGeneration() }

// LeaderAcquisitions, LeaderReleases, and LeaderElectionFailures are
// cumulative counters passed through from the underlying LeaderElector.
func (s *Scheduler) LeaderAcquisitions() int64     { return s.leader.Acquisitions() }
func (s *Scheduler) LeaderReleases() int64         { return s.leader.Releases() }
func (s *Scheduler) LeaderElectionFailures() int64 { return s.leader.ElectionFailures() }
