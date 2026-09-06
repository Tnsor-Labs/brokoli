package taskruntime

import (
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
)

// Requirements is the server-derived placement predicate ADR-033
// section 5 describes as an AND-chain (protocol, runtime+version,
// adapter, io, isolation, resources) -- built by a caller from a
// ResolvedExecutionRecord plus the selected bundle payload's own
// declared requirements and the execution profile's resource policy.
// This package does not derive Requirements itself: nothing here reads
// a task-bundle-v2 manifest or a resolved execution record together, by
// design -- that assembly is the "physical planning" step ADR-033
// section 4 describes, out of scope for this rollout phase.
//
// A zero-value field means "unconstrained" for that clause -- e.g. an
// empty RuntimeClass never checks runtime capability at all, which is
// correct for a code-node-only worker capability document that legally
// has no Runtimes entries.
type Requirements struct {
	Protocol     string
	RuntimeClass RuntimeClass
	// RuntimeVersion is one exact, concrete version (e.g. "3.11.9"), not
	// a range -- range matching against a bundle payload's declared
	// 'requires' happens before Requirements is constructed.
	RuntimeVersion string
	// MinAdapterVersion is an inclusive semver lower bound (e.g. "1.2.0",
	// with or without a leading "v"). Empty means unconstrained.
	MinAdapterVersion string
	IOMode            IOMode
	// Isolation names one required mechanism, checked by exact set
	// membership against WorkerCapabilities.Isolation -- not an ordering
	// comparison. ADR-033 section 5's own worked example writes
	// "isolation >=process", implying some strength ordering exists, but
	// section 12 only names trust profiles (trusted/tenant/untrusted),
	// never a numeric rank between mechanism name strings. Inventing
	// that ordering here would be this package asserting a policy ADR-033
	// itself doesn't state; membership matching is the honest v1
	// behavior until a later phase resolves trust profiles into a real
	// mechanism-strength table.
	Isolation      string
	MinCPUMillis   int64
	MinMemoryBytes int64
}

// MatchResult is Match's verdict. Reason is populated only when Matched
// is false, naming the first AND-clause (in the order ADR-033 section
// 5's own worked example lists them) that failed.
type MatchResult struct {
	Matched bool
	Reason  string
}

func matched() MatchResult { return MatchResult{Matched: true} }

func unmatched(format string, args ...interface{}) MatchResult {
	return MatchResult{Matched: false, Reason: fmt.Sprintf(format, args...)}
}

// Match reports whether capabilities satisfies requirements, per
// ADR-033 section 5's placement predicate. Pure and side-effect-free:
// it consults nothing beyond its two arguments, so it is exactly as
// testable offline as it will be once a real worker fleet exists.
func Match(req Requirements, caps WorkerCapabilities) MatchResult {
	if req.Protocol != "" && !caps.HasProtocol(req.Protocol) {
		return unmatched("worker %s does not advertise protocol %q", caps.WorkerID, req.Protocol)
	}

	if req.RuntimeClass != "" {
		rc, ok := caps.Runtime(req.RuntimeClass)
		if !ok {
			return unmatched("worker %s has no %q runtime capability", caps.WorkerID, req.RuntimeClass)
		}
		if req.RuntimeVersion != "" && !caps.HasRuntimeVersion(req.RuntimeClass, req.RuntimeVersion) {
			return unmatched("worker %s's %q runtime does not include version %q", caps.WorkerID, req.RuntimeClass, req.RuntimeVersion)
		}
		if req.MinAdapterVersion != "" {
			if rc.Adapter == "" {
				return unmatched("worker %s's %q runtime reports no adapter version, but >=%s is required", caps.WorkerID, req.RuntimeClass, req.MinAdapterVersion)
			}
			if semver.Compare(canonicalSemver(rc.Adapter), canonicalSemver(req.MinAdapterVersion)) < 0 {
				return unmatched("worker %s's %q adapter %s is older than the required minimum %s", caps.WorkerID, req.RuntimeClass, rc.Adapter, req.MinAdapterVersion)
			}
		}
	}

	if req.IOMode != "" && !caps.HasIO(req.IOMode) {
		return unmatched("worker %s does not support io mode %q", caps.WorkerID, req.IOMode)
	}

	if req.Isolation != "" && !caps.HasIsolation(req.Isolation) {
		return unmatched("worker %s does not support isolation mechanism %q", caps.WorkerID, req.Isolation)
	}

	if req.MinCPUMillis > 0 && caps.Resources.CPUMillis < req.MinCPUMillis {
		return unmatched("worker %s cpu_millis %d is below the required minimum %d", caps.WorkerID, caps.Resources.CPUMillis, req.MinCPUMillis)
	}

	if req.MinMemoryBytes > 0 && caps.Resources.MemoryBytes < req.MinMemoryBytes {
		return unmatched("worker %s memory_bytes %d is below the required minimum %d", caps.WorkerID, caps.Resources.MemoryBytes, req.MinMemoryBytes)
	}

	return matched()
}

// canonicalSemver prepends "v" when absent -- ADR-033's own examples
// write adapter versions without one (e.g. "1.2.0"), but
// golang.org/x/mod/semver requires it.
func canonicalSemver(v string) string {
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}
