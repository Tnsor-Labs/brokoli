package taskruntime

import (
	"os"
	"testing"
)

// TestADR033WorkedExample reproduces ADR-033 section 5's own placement
// predicate example verbatim:
//
//	protocol task-runtime/v1 AND runtime python@3.11.9 AND adapter >=1.2
//	AND io batch-reference AND isolation >=process AND memory >=512MiB
//
// against the worker-17 capabilities example the same section shows,
// which the ADR's own prose implies should match.
func TestADR033WorkedExample(t *testing.T) {
	data, err := os.ReadFile("../../docs/schema/fixtures/worker-capabilities-v1/positive/mixed-fleet-worker.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	caps, err := ParseWorkerCapabilities(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	req := Requirements{
		Protocol:          "brokoli.task-runtime/v1",
		RuntimeClass:      RuntimeClassPython,
		RuntimeVersion:    "3.11.9",
		MinAdapterVersion: "1.2",
		IOMode:            IOModeBatchReference,
		Isolation:         "process",
		MinMemoryBytes:    512 << 20, // 512MiB
	}

	result := Match(req, *caps)
	if !result.Matched {
		t.Fatalf("expected the ADR's own worked example to match, got: %s", result.Reason)
	}
}

func baseCaps() WorkerCapabilities {
	return WorkerCapabilities{
		WorkerID:  "worker-1",
		Protocols: []string{"brokoli.instance/v2", "brokoli.task-runtime/v1"},
		Platform:  Platform{OS: "linux", Arch: "amd64"},
		Runtimes: []RuntimeCapability{
			{Class: RuntimeClassPython, Versions: []string{"3.11.9"}, Adapter: "1.2.0"},
		},
		IO:        []IOMode{IOModeBatchReference},
		Isolation: []string{"process"},
		Resources: Resources{CPUMillis: 8000, MemoryBytes: 8 << 30},
	}
}

func TestMatch_ZeroValueRequirementsAlwaysMatches(t *testing.T) {
	result := Match(Requirements{}, baseCaps())
	if !result.Matched {
		t.Fatalf("expected an all-unconstrained requirement to match, got: %s", result.Reason)
	}
}

func TestMatch_MissingProtocol(t *testing.T) {
	result := Match(Requirements{Protocol: "brokoli.other/v1"}, baseCaps())
	if result.Matched {
		t.Fatal("expected a mismatch for an unadvertised protocol")
	}
}

func TestMatch_MissingRuntimeClass(t *testing.T) {
	result := Match(Requirements{RuntimeClass: RuntimeClassNode}, baseCaps())
	if result.Matched {
		t.Fatal("expected a mismatch for a runtime class the worker doesn't advertise")
	}
}

func TestMatch_WrongRuntimeVersion(t *testing.T) {
	result := Match(Requirements{RuntimeClass: RuntimeClassPython, RuntimeVersion: "3.12.0"}, baseCaps())
	if result.Matched {
		t.Fatal("expected a mismatch for a runtime version the worker doesn't advertise")
	}
}

func TestMatch_AdapterTooOld(t *testing.T) {
	result := Match(Requirements{RuntimeClass: RuntimeClassPython, MinAdapterVersion: "2.0.0"}, baseCaps())
	if result.Matched {
		t.Fatal("expected a mismatch for an adapter version below the required minimum")
	}
}

func TestMatch_AdapterExactlyAtMinimumMatches(t *testing.T) {
	result := Match(Requirements{RuntimeClass: RuntimeClassPython, MinAdapterVersion: "1.2.0"}, baseCaps())
	if !result.Matched {
		t.Fatalf("expected an adapter exactly at the minimum to match, got: %s", result.Reason)
	}
}

func TestMatch_AdapterVersionWithoutVPrefixComparesCorrectly(t *testing.T) {
	// ADR-033's own examples write adapter versions without a leading
	// "v" (e.g. "1.2.0"); golang.org/x/mod/semver requires one internally
	// -- canonicalSemver must bridge that without the caller ever
	// noticing.
	caps := baseCaps()
	result := Match(Requirements{RuntimeClass: RuntimeClassPython, MinAdapterVersion: "1.1.9"}, caps)
	if !result.Matched {
		t.Fatalf("expected 1.2.0 >= 1.1.9 to match, got: %s", result.Reason)
	}
}

func TestMatch_MissingAdapterVersionWhenRequired(t *testing.T) {
	caps := baseCaps()
	caps.Runtimes[0].Adapter = ""
	result := Match(Requirements{RuntimeClass: RuntimeClassPython, MinAdapterVersion: "1.0.0"}, caps)
	if result.Matched {
		t.Fatal("expected a mismatch when the worker reports no adapter version at all")
	}
}

func TestMatch_UnsupportedIOMode(t *testing.T) {
	result := Match(Requirements{IOMode: IOModeStreamV1}, baseCaps())
	if result.Matched {
		t.Fatal("expected a mismatch for an unsupported io mode")
	}
}

func TestMatch_UnsupportedIsolation(t *testing.T) {
	result := Match(Requirements{Isolation: "container"}, baseCaps())
	if result.Matched {
		t.Fatal("expected a mismatch for an unsupported isolation mechanism")
	}
}

func TestMatch_IsolationIsMembershipNotOrdering(t *testing.T) {
	// A worker advertising only "container" does NOT satisfy a
	// requirement for "process" by this package's deliberate choice not
	// to invent a strength ordering between mechanism names -- see
	// Requirements.Isolation's doc comment.
	caps := baseCaps()
	caps.Isolation = []string{"container"}
	result := Match(Requirements{Isolation: "process"}, caps)
	if result.Matched {
		t.Fatal("expected exact membership matching, not an implied container>=process ordering")
	}
}

func TestMatch_InsufficientCPU(t *testing.T) {
	result := Match(Requirements{MinCPUMillis: 16000}, baseCaps())
	if result.Matched {
		t.Fatal("expected a mismatch for insufficient cpu_millis")
	}
}

func TestMatch_InsufficientMemory(t *testing.T) {
	result := Match(Requirements{MinMemoryBytes: 16 << 30}, baseCaps())
	if result.Matched {
		t.Fatal("expected a mismatch for insufficient memory_bytes")
	}
}

func TestMatch_CodeNodeOnlyWorkerMatchesOnlyUnconstrainedRuntimeRequirements(t *testing.T) {
	data, err := os.ReadFile("../../docs/schema/fixtures/worker-capabilities-v1/positive/code-node-only-worker.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	caps, err := ParseWorkerCapabilities(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if result := Match(Requirements{Protocol: "brokoli.instance/v2"}, *caps); !result.Matched {
		t.Fatalf("expected an unconstrained-runtime requirement to match a code-node-only worker, got: %s", result.Reason)
	}
	if result := Match(Requirements{RuntimeClass: RuntimeClassPython}, *caps); result.Matched {
		t.Fatal("expected a python runtime requirement to reject a worker with zero runtime capabilities")
	}
}
