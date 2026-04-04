package engine

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/hc12r/broked/models"
	"github.com/hc12r/broked/store"
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
}

// NewScheduler creates a scheduler that uses the given engine to run pipelines.
func NewScheduler(engine *Engine, s store.Store) *Scheduler {
	return &Scheduler{
		cron:      cron.New(cron.WithLocation(time.UTC)),
		engine:    engine,
		store:     s,
		entries:   make(map[string]cron.EntryID),
		schedules: make(map[string]string),
		names:     make(map[string]string),
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
	log.Printf("Scheduler started: %d pipelines scheduled", registered)

	return nil
}

// catchUpMissedRuns checks each scheduled pipeline's last run vs its schedule.
// If a run was missed during downtime, triggers it now.
func (s *Scheduler) catchUpMissedRuns(pipelines []models.Pipeline) {
	for _, p := range pipelines {
		if !p.Enabled || p.Schedule == "" {
			continue
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
			log.Printf("Catch-up: pipeline %q missed scheduled run at %s, triggering now",
				p.Name, nextExpected.Format(time.RFC3339))
			go func(pid string) {
				if _, err := s.engine.RunPipeline(pid); err != nil {
					log.Printf("ERROR: catch-up run failed for %s: %v", pid, err)
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
		// Check cross-pipeline dependencies before running
		if pipe, pErr := s.engine.store.GetPipeline(pid); pErr == nil && len(pipe.DependsOn) > 0 {
			for _, depID := range pipe.DependsOn {
				runs, _ := s.engine.store.ListRunsByPipeline(depID, 1)
				if len(runs) == 0 || string(runs[0].Status) != "completed" {
					depName := depID[:8]
					if dp, e := s.engine.store.GetPipeline(depID); e == nil {
						depName = dp.Name
					}
					log.Printf("Skipping scheduled run for %s: dependency %q not satisfied (last: %s)", pid, depName, func() string { if len(runs) > 0 { return string(runs[0].Status) }; return "no runs" }())
					return
				}
			}
		}
		log.Printf("Scheduled run triggered for pipeline %s", pid)
		if _, err := s.engine.RunPipeline(pid); err != nil {
			log.Printf("ERROR: scheduled run failed for pipeline %s: %v", pid, err)
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
