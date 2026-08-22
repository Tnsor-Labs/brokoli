package cmd

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// docs/adr/018-chunked-execution-and-backpressure.md: every existing
// admission control in the engine (maxParallel, BROKOLI_MAX_CONCURRENT_RUNS,
// workerSlots below) bounds how many things run, never how much memory
// they're allowed to use — a deployment sized for a few concurrent
// lightweight pipelines OOMs on the same count of heavy ones, and nothing
// in the system could tell those two cases apart before the kernel's OOM
// killer did. Found live: 10 concurrent runs of a 100k-row pipeline against
// 6 worker pods with a 512Mi limit each OOMKilled repeatedly, well within
// BROKOLI_MAX_CONCURRENT_RUNS's per-pod count ceiling — the count was never
// the constraint, the memory was.
//
// This file adds a second, independent admission gate a worker checks
// before claiming a new job: real, measured memory headroom against the
// container's own cgroup limit, not a predicted or configured one. "Out of
// the box" is the point — no operator has to estimate their heaviest
// pipeline's memory footprint and hand-tune a concurrency count around it;
// the worker simply stops asking for more work when it's genuinely close to
// its ceiling, and resumes once headroom returns (a run that couldn't be
// admitted anywhere stays `pending`, visible via the existing
// brokoli_runs_queue_waiting metric — this is backpressure into the queue
// that already exists, not a new one).

// memoryBackpressureSafetyMarginFraction is the fraction of the container's
// memory limit that must remain free for a worker to claim a new job.
//
// Deliberately conservative: this worker's own in-flight jobs (there may
// already be several, per workerCount) and this decision are the last
// checkpoint before the kernel's OOM killer, not a fine-tuned optimization
// target — better to leave real headroom unused occasionally than to admit
// the one job that tips a pod over. A canary deployment should inform
// whether this default is right in practice, per the ADR's own "Negative"
// consequence about needing real tuning.
var memoryBackpressureSafetyMarginFraction = 0.20

// memoryBackpressureMinMarginBytes floors the safety margin for small
// containers, where 20% of the limit alone could be a few MiB — not enough
// headroom for even one more concurrent job's own baseline footprint
// regardless of the fraction.
var memoryBackpressureMinMarginBytes int64 = 64 << 20 // 64 MiB

// memoryBackpressureRecheckInterval is how often a worker withholding
// admission re-checks headroom. Short enough that a worker resumes quickly
// once in-flight jobs finish and free memory; long enough not to spin.
var memoryBackpressureRecheckInterval = 2 * time.Second

// memoryBackpressureSettleDelay is the minimum time a worker waits after
// successfully admitting one job before it will admit another, regardless
// of what hasMemoryHeadroom reports in between.
//
// A cgroup memory reading is a snapshot of usage *right now* — it says
// nothing about a job that was just admitted but hasn't started allocating
// yet. Found live re-testing this exact backpressure change: a burst of
// simultaneous requests could still land two jobs on the same worker
// roughly a second apart, because the second admission's headroom check
// ran before the first job's memory footprint had ramped up enough to be
// visible — both checks saw a pod that still looked idle. Without this
// delay, point-in-time admission control cannot see a job it just started;
// this trades a small amount of throughput under a genuine burst for
// actually closing that race, rather than only helping the slower,
// staggered-arrival case a bare headroom check already covers.
var memoryBackpressureSettleDelay = 3 * time.Second

// cgroupMemoryV2Dir and cgroupMemoryV1Dir are the standard cgroup mount
// points. Package vars, not consts, so tests can point them at a temp
// directory holding fake cgroup files instead of depending on actually
// running inside a container.
var (
	cgroupMemoryV2Dir = "/sys/fs/cgroup"
	cgroupMemoryV1Dir = "/sys/fs/cgroup/memory"
)

// memoryBackpressureDisabled is read once per process via
// BROKOLI_MEMORY_BACKPRESSURE_DISABLED=1 — an escape hatch for an operator
// who has already sized their deployment generously and wants the
// unconditional pre-this-change behavior back, or who hits a false
// positive this heuristic doesn't handle well for their workload.
func memoryBackpressureDisabled() bool {
	return os.Getenv("BROKOLI_MEMORY_BACKPRESSURE_DISABLED") == "1"
}

// hasMemoryHeadroom reports whether this process currently has enough free
// memory, relative to its container's own limit, to safely accept one more
// unit of work.
//
// Fails open — returns true — whenever the limit or current usage can't be
// determined: no cgroup mount (a bare host, most local dev environments,
// macOS), an unlimited ("max") cgroup v2 limit, or any read error. This is
// a strict addition to existing admission control, never a replacement for
// it; an environment this can't reason about must behave exactly as it did
// before this existed, not fail closed and refuse all work.
// memoryBackpressureComfortMultiple is how many times the safety margin
// of free memory counts as "comfortable": enough that admitting one more
// job cannot plausibly tip the pod over before that job's own usage
// becomes visible in the cgroup reading.
//
// The settle delay below exists because a cgroup reading cannot see a job
// admitted a moment ago. That reasoning holds when the worker is near its
// ceiling; it does not when most of the limit is free, and applying the
// delay unconditionally cost far more than it protected. Measured on a
// worker with ~75% of its limit free: eight short runs across two workers
// took 11.7s wall for 1.8s of actual work, because each worker sat out
// three seconds between admissions. That is a throughput ceiling of one
// run per settle delay per worker, independent of how small the runs are.
var memoryBackpressureComfortMultiple = 3.0

// admissionDecision reports whether a worker may claim another job now,
// and when it may not, how long to wait before asking again.
//
// Three states: genuinely tight on memory (wait and re-check), close
// enough that a just-admitted job could still tip the balance (wait out
// the remainder of the settle delay, not a fixed re-check interval), or
// comfortable (admit immediately).
func admissionDecision(lastAdmission time.Time) (admit bool, wait time.Duration) {
	if memoryBackpressureDisabled() {
		return true, 0
	}
	limit, current, ok := cgroupMemoryUsage()
	if !ok || limit <= 0 {
		return true, 0
	}

	margin := int64(float64(limit) * memoryBackpressureSafetyMarginFraction)
	if margin < memoryBackpressureMinMarginBytes {
		margin = memoryBackpressureMinMarginBytes
	}
	headroom := limit - current

	if headroom <= margin {
		return false, memoryBackpressureRecheckInterval
	}
	if float64(headroom) < float64(margin)*memoryBackpressureComfortMultiple {
		if since := time.Since(lastAdmission); since < memoryBackpressureSettleDelay {
			// Sleep exactly what is left rather than a flat re-check
			// interval, which rounded a 3s delay up to 4s in practice.
			return false, memoryBackpressureSettleDelay - since
		}
	}
	return true, 0
}

func hasMemoryHeadroom() bool {
	if memoryBackpressureDisabled() {
		return true
	}
	limit, current, ok := cgroupMemoryUsage()
	if !ok || limit <= 0 {
		return true
	}
	margin := int64(float64(limit) * memoryBackpressureSafetyMarginFraction)
	if margin < memoryBackpressureMinMarginBytes {
		margin = memoryBackpressureMinMarginBytes
	}
	headroom := limit - current
	return headroom > margin
}

// cgroupMemoryUsage reads (limit, current usage) in bytes from cgroup v2
// first, falling back to v1 — matching how Kubernetes itself picks whichever
// the node's kernel and container runtime negotiated, without the caller
// needing to know which one it is.
func cgroupMemoryUsage() (limit, current int64, ok bool) {
	if limit, current, ok = cgroupMemoryUsageV2(); ok {
		return limit, current, true
	}
	return cgroupMemoryUsageV1()
}

func cgroupMemoryUsageV2() (limit, current int64, ok bool) {
	limit, limitOK := readCgroupInt(cgroupMemoryV2Dir + "/memory.max")
	current, currentOK := readCgroupInt(cgroupMemoryV2Dir + "/memory.current")
	return limit, current, limitOK && currentOK
}

func cgroupMemoryUsageV1() (limit, current int64, ok bool) {
	limit, limitOK := readCgroupInt(cgroupMemoryV1Dir + "/memory.limit_in_bytes")
	current, currentOK := readCgroupInt(cgroupMemoryV1Dir + "/memory.usage_in_bytes")
	if !limitOK || !currentOK {
		return 0, 0, false
	}
	// An unconfined cgroup v1 limit reads back as a huge sentinel
	// (typically close to the architecture's max signed 64-bit value,
	// rounded down to a page boundary) rather than a literal "max" string
	// the way v2 does — there is no fixed number to match exactly across
	// kernels, so anything absurdly larger than any real container limit
	// is treated the same as v2's "max": no usable limit, fail open.
	const cgroupV1UnconfinedThreshold = int64(1) << 62
	if limit >= cgroupV1UnconfinedThreshold {
		return 0, 0, false
	}
	return limit, current, true
}

// readCgroupInt reads a cgroup pseudo-file expected to hold either a single
// integer or the literal string "max" (cgroup v2's spelling of "no limit").
func readCgroupInt(path string) (int64, bool) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is one of two fixed cgroup mount points (or a test-overridden var pointed at a temp dir), never built from request/caller input.
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(data))
	if s == "max" {
		return 0, false // unlimited: no usable ceiling to compare headroom against
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
