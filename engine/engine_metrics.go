package engine

import "sync/atomic"

// EngineMetrics is a snapshot of the engine's counters.
type EngineMetrics struct {
	RunsTotal, RunsSucceeded, RunsFailed, RunsCancelled     int64
	RunsRecovered, RunsRecoveryFailed, RunsRecoveryRequeued int64
	AttemptsReclaimed                                       int64
	EventsAppended, EventsAppendFailed, EventsReplayed      int64
	AttemptsStarted, AttemptsSucceeded                      int64
	AttemptsFailed, AttemptsRetried                         int64
	RunsQueueWaiting                                        int64
}

// Metrics reads the engine's counters. They are updated with atomic adds
// while runs execute, so a plain read of the exported fields during a run
// is a data race; this reads each one atomically.
func (e *Engine) Metrics() EngineMetrics {
	return EngineMetrics{
		RunsTotal:            atomic.LoadInt64(&e.RunsTotal),
		RunsSucceeded:        atomic.LoadInt64(&e.RunsSucceeded),
		RunsFailed:           atomic.LoadInt64(&e.RunsFailed),
		RunsCancelled:        atomic.LoadInt64(&e.RunsCancelled),
		RunsRecovered:        atomic.LoadInt64(&e.RunsRecovered),
		RunsRecoveryFailed:   atomic.LoadInt64(&e.RunsRecoveryFailed),
		RunsRecoveryRequeued: atomic.LoadInt64(&e.RunsRecoveryRequeued),
		AttemptsReclaimed:    atomic.LoadInt64(&e.AttemptsReclaimed),
		EventsAppended:       atomic.LoadInt64(&e.EventsAppended),
		EventsAppendFailed:   atomic.LoadInt64(&e.EventsAppendFailed),
		EventsReplayed:       atomic.LoadInt64(&e.EventsReplayed),
		AttemptsStarted:      atomic.LoadInt64(&e.AttemptsStarted),
		AttemptsSucceeded:    atomic.LoadInt64(&e.AttemptsSucceeded),
		AttemptsFailed:       atomic.LoadInt64(&e.AttemptsFailed),
		AttemptsRetried:      atomic.LoadInt64(&e.AttemptsRetried),
		RunsQueueWaiting:     atomic.LoadInt64(&e.RunsQueueWaiting),
	}
}
