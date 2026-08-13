package cmd

import (
	"log"
	"math"
	"os"
	"runtime/debug"
)

// This file is the second half of docs/adr/018-chunked-execution-and-
// backpressure.md's "out of the box" resource story, alongside
// memory_backpressure.go's admission gate. Both were motivated by the same
// live finding: a 512Mi worker pod OOMKills under ordinary concurrent load
// with every knob at its default, and nothing in the system adapts any
// default to the container it actually landed in.
//
// Two adaptations, both derived from the container's own cgroup memory
// limit, both no-ops when no limit is readable (bare host, dev laptop):
//
//  1. GOMEMLIMIT. Without it, Go's collector paces itself off GOGC alone
//     (default 100: let the heap roughly double past the live set before
//     collecting) with no idea a hard ceiling exists — on a small pod the
//     runtime happily sails heap+garbage straight through the cgroup limit
//     and the kernel's OOM killer fires where an aggressive GC cycle would
//     have sufficed. Setting GOMEMLIMIT to just under the cgroup limit
//     converts that class of death into GC pressure, which is exactly the
//     trade a memory-constrained pod wants. This is the standard, Go-team-
//     recommended posture for containerized Go since 1.19; it simply was
//     never wired here.
//
//  2. The default for how many whole runs one process executes at once.
//     BROKOLI_MAX_CONCURRENT_RUNS has always defaulted to 4 whether the
//     pod has 512Mi or 64Gi — the count was never the real constraint, the
//     memory behind it was (the same observation that motivated the
//     admission gate). With queuing and elastic scaling now in place
//     (EE-ADR-008), the correct posture for a small pod is to take FEWER
//     jobs and let demand surface as queue depth the autoscaler answers
//     with more replicas — not to overcommit one pod because a hardcoded
//     default said 4.

// autoMemLimitFraction is how much of the cgroup memory limit GOMEMLIMIT
// is set to. The gap is headroom for what the Go runtime's accounting
// doesn't cover (CGO allocations from the sqlite driver, goroutine stacks'
// OS overhead, the binary itself) plus room for the GC to actually operate
// before the kernel's hard ceiling. 0.9 is the widely used default for
// exactly this pattern.
var autoMemLimitFraction = 0.9

// memoryPerConcurrentRunBytes is the working assumption behind the
// adaptive concurrency default: one in-flight run of a "normal" pipeline
// deserves roughly this much of the pod's memory budget. Derived from this
// session's own live numbers — a 100k-row pipeline's live footprint sits
// in the low hundreds of MiB with spilling active — deliberately round and
// conservative rather than tuned to one benchmark. Only shapes the DEFAULT;
// an explicit BROKOLI_MAX_CONCURRENT_RUNS always wins.
var memoryPerConcurrentRunBytes = int64(256 << 20) // 256 MiB

// defaultMaxConcurrentRunsCap keeps the adaptive default from silently
// exceeding what the old fixed default ever allowed on huge machines —
// raising the ceiling is an operator decision (set the env var), not
// something a heuristic should do on its own.
const defaultMaxConcurrentRunsCap = 4

// applyAutoMemoryLimit sets the Go runtime's soft memory limit from the
// container's cgroup limit, unless the operator already set one.
//
// An explicit GOMEMLIMIT env var is detected through the runtime itself
// (the runtime parses it at process start; SetMemoryLimit(-1) reads the
// current value without changing it) rather than by re-parsing the env —
// so GOMEMLIMIT=2GiB, GOMEMLIMIT=off-by-huge-value, and anything else Go
// accepts all count as "operator decided, leave it alone".
//
// Returns the limit it applied, or 0 if it left things untouched (for the
// startup log and tests).
func applyAutoMemoryLimit() int64 {
	if debug.SetMemoryLimit(-1) != math.MaxInt64 {
		return 0 // operator set GOMEMLIMIT; theirs wins
	}
	limit, _, ok := cgroupMemoryUsage()
	if !ok || limit <= 0 {
		return 0 // no readable ceiling (bare host, dev laptop): keep Go's default
	}
	target := int64(float64(limit) * autoMemLimitFraction)
	if target <= 0 {
		return 0
	}
	debug.SetMemoryLimit(target)
	return target
}

// adaptiveMaxConcurrentRuns returns the memory-derived default for how
// many whole runs this process should execute concurrently, and whether
// the caller should apply it. False when the operator set
// BROKOLI_MAX_CONCURRENT_RUNS explicitly (engine.NewEngine already honored
// it — their number, their pods) or when no cgroup limit is readable (the
// existing fixed default stands, exactly as before this existed).
func adaptiveMaxConcurrentRuns() (int, bool) {
	if os.Getenv("BROKOLI_MAX_CONCURRENT_RUNS") != "" {
		return 0, false
	}
	limit, _, ok := cgroupMemoryUsage()
	if !ok || limit <= 0 {
		return 0, false
	}
	n := int(limit / memoryPerConcurrentRunBytes)
	if n < 1 {
		n = 1
	}
	if n > defaultMaxConcurrentRunsCap {
		n = defaultMaxConcurrentRunsCap
	}
	return n, true
}

// applyAdaptiveResourceDefaults wires both adaptations in, with one log
// line each so an operator reading startup logs can see exactly what was
// derived and from where. Called once from serve's RunE, after the engine
// exists and before anything dispatches work.
func applyAdaptiveResourceDefaults(setMaxConcurrentRuns func(int)) {
	if applied := applyAutoMemoryLimit(); applied > 0 {
		log.Printf("GOMEMLIMIT: auto-set to %d MiB (%.0f%% of the container's cgroup memory limit); set GOMEMLIMIT explicitly to override", applied>>20, autoMemLimitFraction*100)
	}
	if n, ok := adaptiveMaxConcurrentRuns(); ok {
		log.Printf("Max concurrent runs: defaulting to %d for this container's memory limit; set BROKOLI_MAX_CONCURRENT_RUNS to override", n)
		setMaxConcurrentRuns(n)
	}
}
