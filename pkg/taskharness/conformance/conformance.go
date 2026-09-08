// Package conformance is the shared behaviour suite every
// brokoli.task-runtime/v1 reference adapter must satisfy (ADR-033
// section 19).
//
// It exists because three adapters had drifted into testing different
// things. Before this package: pyharness had 3 end-to-end tests,
// nodeharness 8 and jvmharness 11, with no two covering the same set --
// "the declared interface is authoritative" was checked in one adapter
// only, and the primary one had the least coverage. Three
// implementations of one protocol tested three different ways is how a
// protocol becomes three dialects.
//
// The CASES live here; the plumbing to run one stays in each adapter's
// own package, because the invocation descriptor is deliberately
// per-adapter (a python sys.path, node module roots, a JVM classpath).
// So this package owns what must be true, and each adapter owns how to
// ask.
//
// Where adapters legitimately differ, the difference is declared rather
// than omitted: ExpectFailure records that a case fails on a named
// runtime and why. That turns "node cannot do this" from an absence
// nobody notices into a fact the suite asserts.
package conformance

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/taskharness"
)

// Runtime class names, mirroring taskbundlev2's own constants. Not
// imported from there to keep this package free of bundle concerns --
// it tests harnesses, not bundles.
const (
	Python = "python"
	Node   = "node"
	JVM    = "jvm"
)

// Case is one behaviour every adapter must exhibit identically, unless
// ExpectFailure says otherwise for a named runtime.
type Case struct {
	// Name is the behaviour, phrased as the property being asserted.
	Name string
	// Source is the task's module source per runtime class. A runtime
	// with no entry is skipped for this case, which is how a case that
	// genuinely cannot be expressed in one language stays honest instead
	// of being faked.
	Source map[string]string
	// OutputKind is the port's declared ADR-032 section 6 kind; empty
	// means scalar.
	OutputKind string
	// OutputMediaType labels an artifact output.
	OutputMediaType string
	// InputNDJSON is staged as the task's input when non-empty.
	InputNDJSON string
	// ExpectFailure maps a runtime class to the failure category that
	// runtime must report instead of succeeding, with Reason explaining
	// why the difference is legitimate.
	ExpectFailure map[string]ExpectedFailure
	// Assert checks a successful result. resultJSON is the raw
	// task-result-v1 bytes -- raw, because comparing decoded numbers is
	// how brokoli#479 and #492 stayed hidden.
	Assert func(t *testing.T, res taskharness.Result, resultJSON []byte)
}

// ExpectedFailure declares that one runtime cannot satisfy a case.
type ExpectedFailure struct {
	// Category is the ADR-033 section 14 failure category expected.
	Category string
	// Reason is why this runtime legitimately differs. Required: an
	// undocumented expected failure is indistinguishable from a bug
	// somebody stopped chasing.
	Reason string
}

// Runner executes one case against a specific adapter and returns the
// protocol result plus the raw task-result-v1 bytes ("" when the run
// failed before writing one).
type Runner func(t *testing.T, c Case) (taskharness.Result, []byte)

// Run executes every case that names sourceKey against the adapter.
func Run(t *testing.T, runtimeClass string, run Runner) {
	t.Helper()
	for _, c := range Cases() {
		source, ok := c.Source[runtimeClass]
		if !ok || source == "" {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			res, resultJSON := run(t, c)
			if want, differs := c.ExpectFailure[runtimeClass]; differs {
				if res.Failure == nil {
					t.Fatalf("expected this runtime to fail (%s), but it succeeded", want.Reason)
				}
				if res.Failure.Category != want.Category {
					t.Errorf("failure category = %q, want %q (%s)", res.Failure.Category, want.Category, want.Reason)
				}
				return
			}
			if res.Failure != nil {
				t.Fatalf("expected success, got %s: %s", res.Failure.Category, res.Failure.Message)
			}
			if c.Assert != nil {
				c.Assert(t, res, resultJSON)
			}
		})
	}
}

// port pulls the single "result" output port out of a task-result-v1
// document.
func port(t *testing.T, resultJSON []byte) map[string]interface{} {
	t.Helper()
	var doc struct {
		Contract string                            `json:"contract"`
		Outputs  map[string]map[string]interface{} `json:"outputs"`
	}
	if err := json.Unmarshal(resultJSON, &doc); err != nil {
		t.Fatalf("result is not valid JSON: %v\n%s", err, resultJSON)
	}
	if doc.Contract != "brokoli.task-result/v1" {
		t.Errorf("contract = %q, want brokoli.task-result/v1", doc.Contract)
	}
	p, ok := doc.Outputs["result"]
	if !ok {
		t.Fatalf("result has no 'result' output port: %s", resultJSON)
	}
	return p
}

func wantKind(t *testing.T, p map[string]interface{}, kind string) {
	t.Helper()
	if p["kind"] != kind {
		t.Errorf("output kind = %v, want %q", p["kind"], kind)
	}
}

// Cases is the suite. Adding one here obliges every adapter that can
// express it, which is the point.
func Cases() []Case {
	return []Case{
		{
			Name: "a scalar result round-trips",
			Source: map[string]string{
				Python: "def run():\n    return 42\n",
				Node:   "export function run() {\n  return 42;\n}\n",
				JVM:    "public final class FixtureTask {\n    public static Object run() { return 42L; }\n}\n",
			},
			Assert: func(t *testing.T, _ taskharness.Result, raw []byte) {
				p := port(t, raw)
				wantKind(t, p, "scalar")
				if !strings.Contains(string(raw), "42") {
					t.Errorf("result does not carry 42: %s", raw)
				}
			},
		},
		{
			// brokoli#479 lost this value in Go; #492 found the same class
			// live in the Node harness. Compared as TEXT: in JavaScript
			// `v === 9007199254740993` is true after the value has been
			// altered, because the comparison literal rounds identically.
			Name: "a 64-bit integer survives input and output",
			Source: map[string]string{
				Python: "def run(input):\n    return input[0]['id']\n",
				Node:   "export function run({ input }) {\n  return input[0].id;\n}\n",
				JVM: "import java.util.*;\npublic final class FixtureTask {\n" +
					"    @SuppressWarnings(\"unchecked\")\n" +
					"    public static Object run(Map<String, Object> kwargs) {\n" +
					"        List<Object> rows = (List<Object>) kwargs.get(\"input\");\n" +
					"        return ((Map<String, Object>) rows.get(0)).get(\"id\");\n" +
					"    }\n}\n",
			},
			InputNDJSON: "{\"id\":9007199254740993}\n",
			// No exemption for Node any more. It first refused this value
			// (#494) and now carries it as a BigInt (#496): JavaScript has
			// no wider number, so the harness handles the literal before
			// JSON.parse can destroy it. Removing the exemption is what
			// makes that a verified claim rather than a changelog line.
			Assert: func(t *testing.T, _ taskharness.Result, raw []byte) {
				if !strings.Contains(string(raw), "9007199254740993") {
					t.Errorf("the exact value did not survive: %s", raw)
				}
				if strings.Contains(string(raw), "9007199254740992") {
					t.Errorf("value altered by one -- the #479 corruption: %s", raw)
				}
			},
		},
		{
			Name:       "a dataset output is described by reference",
			OutputKind: "dataset",
			Source: map[string]string{
				Python: "def run():\n    return [{'id': 7}]\n",
				Node:   "export function run() {\n  return [{ id: 7 }];\n}\n",
				JVM: "import java.util.*;\npublic final class FixtureTask {\n" +
					"    public static Object run() {\n" +
					"        Map<String, Object> r = new LinkedHashMap<>();\n" +
					"        r.put(\"id\", 7L);\n" +
					"        return new ArrayList<>(List.of(r));\n" +
					"    }\n}\n",
			},
			Assert: func(t *testing.T, _ taskharness.Result, raw []byte) {
				p := port(t, raw)
				wantKind(t, p, "dataset")
				if p["codec"] != "ndjson/v1" {
					t.Errorf("codec = %v, want ndjson/v1", p["codec"])
				}
				// Size and checksum must describe the bytes actually
				// written: the worker verifies against them (ADR-033
				// section 7 rule 6).
				if sum, _ := p["checksum"].(string); !strings.HasPrefix(sum, "sha256:") {
					t.Errorf("checksum = %v, want a sha256 reference", p["checksum"])
				}
				if n, _ := p["size_bytes"].(float64); n <= 0 {
					t.Errorf("size_bytes = %v, want the real byte count", p["size_bytes"])
				}
			},
		},
		{
			Name:            "an artifact output states its media type in codec",
			OutputKind:      "artifact",
			OutputMediaType: "text/plain",
			Source: map[string]string{
				Python: "def run():\n    return b'hello artifact'\n",
				Node:   "export function run() {\n  return Buffer.from('hello artifact');\n}\n",
				JVM:    "public final class FixtureTask {\n    public static Object run() { return \"hello artifact\"; }\n}\n",
			},
			Assert: func(t *testing.T, _ taskharness.Result, raw []byte) {
				p := port(t, raw)
				wantKind(t, p, "artifact")
				// The manifest has no media_type field, so an artifact
				// states its media type in codec.
				if p["codec"] != "text/plain" {
					t.Errorf("codec = %v, want the declared media type", p["codec"])
				}
				if n, _ := p["size_bytes"].(float64); int(n) != len("hello artifact") {
					t.Errorf("size_bytes = %v, want %d", p["size_bytes"], len("hello artifact"))
				}
			},
		},
		{
			Name:       "every collection item carries its key",
			OutputKind: "collection",
			Source: map[string]string{
				Python: "def run():\n    return {'alpha': 1, 'beta': 'two'}\n",
				Node:   "export function run() {\n  return new Map([['alpha', 1], ['beta', 'two']]);\n}\n",
				JVM: "import java.util.*;\npublic final class FixtureTask {\n" +
					"    public static Object run() {\n" +
					"        Map<String, Object> m = new LinkedHashMap<>();\n" +
					"        m.put(\"alpha\", 1L);\n" +
					"        m.put(\"beta\", \"two\");\n" +
					"        return m;\n" +
					"    }\n}\n",
			},
			Assert: func(t *testing.T, _ taskharness.Result, raw []byte) {
				p := port(t, raw)
				wantKind(t, p, "collection")
				items, _ := p["items"].([]interface{})
				if len(items) != 2 {
					t.Fatalf("items = %#v, want two", items)
				}
				// The key is what makes an item separately addressable
				// (ADR-032 section 6) -- required, never positional.
				for _, it := range items {
					m, _ := it.(map[string]interface{})
					if k, _ := m["item_key"].(string); k == "" {
						t.Errorf("item has no item_key: %#v", m)
					}
				}
			},
		},
		{
			// The DECLARED interface decides the kind, never the returned
			// value's runtime shape. Was tested in one adapter only.
			Name:       "the declared kind is authoritative, not the returned shape",
			OutputKind: "dataset",
			Source: map[string]string{
				Python: "def run():\n    return 'not a dataset'\n",
				Node:   "export function run() {\n  return 'not a dataset';\n}\n",
				JVM:    "public final class FixtureTask {\n    public static Object run() { return \"not a dataset\"; }\n}\n",
			},
			ExpectFailure: map[string]ExpectedFailure{
				Python: {Category: taskharness.FailureContractViolation, Reason: "a string is not an iterable of row objects"},
				Node:   {Category: taskharness.FailureContractViolation, Reason: "a string is not an iterable of row objects"},
				JVM:    {Category: taskharness.FailureContractViolation, Reason: "a String is not a list of row maps"},
			},
		},
		{
			Name: "a task that raises reports user_code",
			Source: map[string]string{
				Python: "def run():\n    raise ValueError('boom')\n",
				Node:   "export function run() {\n  throw new Error('boom');\n}\n",
				JVM:    "public final class FixtureTask {\n    public static Object run() { throw new IllegalStateException(\"boom\"); }\n}\n",
			},
			ExpectFailure: map[string]ExpectedFailure{
				Python: {Category: taskharness.FailureUserCode, Reason: "the task itself failed"},
				Node:   {Category: taskharness.FailureUserCode, Reason: "the task itself failed"},
				JVM:    {Category: taskharness.FailureUserCode, Reason: "the task itself failed"},
			},
		},
	}
}
