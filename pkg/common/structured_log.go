package common

import (
	"log/slog"
	"os"
	"strings"
	"sync"
)

// Structured logging for the run/attempt lifecycle hot path
// (Tnsor-Labs/brokoli#11).
//
// pkg/common/logger.go's Logger predates this and remains in place for
// existing free-text call sites across the codebase — migrating every one of
// those is out of scope here (see the PR description). This file adds a
// second, narrower entry point built on the standard library's log/slog
// (available since Go 1.21; this module already targets go 1.25, so this
// pulls in zero new dependencies) specifically so operators can correlate a
// run across API, scheduler, executor, and recovery logs via a shared
// key-value field (run_id/node_id/attempt) instead of free-text
// interpolation, which is the acceptance criterion this issue names.
//
// Call sites on the run/attempt lifecycle hot path (engine/engine.go,
// engine/runner.go, engine/scheduler.go, engine/recovery.go, and
// store/postgres_leader.go's leader election loop) use SLog() plus the
// RunAttr/NodeAttr/AttemptAttr/PipelineAttr/TraceAttr/HolderAttr helpers
// below so every call site spells the correlation keys identically.

var (
	structuredLoggerMu sync.RWMutex
	structuredLogger   = newStructuredLogger()
)

// newStructuredLogger builds the default process-wide structured logger.
// Configuration is via environment variables so operators can switch
// formats/levels without a code change, matching how the rest of this
// package's config (LogFilePath, etc.) is env-driven:
//
//   - BROKOLI_LOG_FORMAT: "json" for slog.NewJSONHandler (machine-parseable,
//     the right choice once log aggregation is in place), anything else
//     (including unset) uses slog.NewTextHandler (human-readable, the
//     current default so local `go run`/`brokoli serve` output doesn't
//     change shape without an operator opting in).
//   - BROKOLI_LOG_LEVEL: DEBUG/INFO/WARN/ERROR, mirroring
//     LogLevelFromString's accepted values. Defaults to INFO.
func newStructuredLogger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: structuredLevelFromEnv()}
	var handler slog.Handler
	if strings.EqualFold(os.Getenv("BROKOLI_LOG_FORMAT"), "json") {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

func structuredLevelFromEnv() slog.Level {
	switch strings.ToUpper(os.Getenv("BROKOLI_LOG_LEVEL")) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARNING", "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// SLog returns the process-wide structured logger used for run/attempt
// lifecycle correlation. Safe for concurrent use.
func SLog() *slog.Logger {
	structuredLoggerMu.RLock()
	defer structuredLoggerMu.RUnlock()
	return structuredLogger
}

// SetStructuredLogger overrides the process-wide structured logger returned
// by SLog. Exists for tests that want to capture and assert on structured
// output (e.g. via slog.NewTextHandler into a bytes.Buffer) without
// depending on process-wide stdout.
func SetStructuredLogger(l *slog.Logger) {
	structuredLoggerMu.Lock()
	defer structuredLoggerMu.Unlock()
	structuredLogger = l
}

// The correlation field helpers below exist so every call site on the
// run/attempt lifecycle hot path spells the same key the same way
// (run_id/node_id/attempt/pipeline_id/trace_id/holder), which is what makes
// filtering logs by one of these fields actually correlate a run across
// API, scheduler, executor, and recovery output — the acceptance criterion
// this issue names. Building them as slog.Attr (not raw key/value pairs)
// keeps call sites terse: SLog().Info("msg", common.RunAttr(id), ...).

// RunAttr identifies the run a log line belongs to.
func RunAttr(runID string) slog.Attr { return slog.String("run_id", runID) }

// NodeAttr identifies the node (within a run) a log line belongs to. Empty
// for run-level (not node-level) log lines.
func NodeAttr(nodeID string) slog.Attr { return slog.String("node_id", nodeID) }

// AttemptAttr identifies the retry attempt number (0-indexed, matching
// models.NodeRun.Attempt) a log line belongs to.
func AttemptAttr(attempt int) slog.Attr { return slog.Int("attempt", attempt) }

// PipelineAttr identifies the pipeline a log line belongs to.
func PipelineAttr(pipelineID string) slog.Attr { return slog.String("pipeline_id", pipelineID) }

// TraceAttr carries the run's internal trace correlation ID (see
// models.Run.TraceID) — distinct from an OpenTelemetry trace ID, though
// both identify the same run's lifecycle.
func TraceAttr(traceID string) slog.Attr { return slog.String("trace_id", traceID) }

// HolderAttr identifies the scheduler-leader-election holder (see
// store.PostgresLeaderElector) a log line belongs to.
func HolderAttr(holderID string) slog.Attr { return slog.String("holder", holderID) }

// GenerationAttr carries a leader-election fencing generation.
func GenerationAttr(generation int64) slog.Attr { return slog.Int64("generation", generation) }
