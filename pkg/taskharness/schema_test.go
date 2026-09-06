package taskharness

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// These tests tie this package's own wire encode/decode to the
// contract fixtures Phase 0 (PR #452) already established at
// docs/schema/fixtures/task-runtime-v1 -- proving Phase 2a's harness
// client speaks the exact protocol Phase 0 specified, not a
// close-enough reinterpretation of it.

const taskRuntimeV1SchemaPath = "../../docs/schema/task-runtime-v1.json"

func compileTaskRuntimeV1Schema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	sch, err := c.Compile(taskRuntimeV1SchemaPath)
	if err != nil {
		t.Fatalf("compile task-runtime-v1.json: %v", err)
	}
	return sch
}

func validateAgainstSchema(t *testing.T, sch *jsonschema.Schema, frame []byte) error {
	t.Helper()
	// Strip the trailing LF encodeFrame adds -- the schema validates one
	// decoded frame's shape, not the wire-level line terminator.
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(bytesTrimLF(frame)))
	if err != nil {
		t.Fatalf("reparse encoded frame: %v", err)
	}
	return sch.Validate(inst)
}

func bytesTrimLF(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\n' {
		return b[:len(b)-1]
	}
	return b
}

func TestEncodedStartFrameValidatesAgainstSchema(t *testing.T) {
	sch := compileTaskRuntimeV1Schema(t)
	frame := NewStartFrame("/attempt/invoke.py", "/attempt/result.json", "/attempt/out")
	b, err := encodeFrame(frame)
	if err != nil {
		t.Fatalf("encodeFrame: %v", err)
	}
	if err := validateAgainstSchema(t, sch, b); err != nil {
		t.Errorf("encoded start frame rejected by task-runtime-v1.json:\n%v", err)
	}
}

func TestEncodedCancelFrameValidatesAgainstSchema(t *testing.T) {
	sch := compileTaskRuntimeV1Schema(t)
	frame := CancelFrame{Type: "cancel", Reason: "context cancelled"}
	b, err := encodeFrame(frame)
	if err != nil {
		t.Fatalf("encodeFrame: %v", err)
	}
	if err := validateAgainstSchema(t, sch, b); err != nil {
		t.Errorf("encoded cancel frame rejected by task-runtime-v1.json:\n%v", err)
	}
}

// TestDecodeFrameLineAcceptsEveryPositiveFixture proves the framing-
// level decoder (duplicate-key/UTF-8/single-object checks) doesn't
// reject any wire content the schema itself accepts -- this package's
// framing rules are meant to be strictly additive to the schema, never
// stricter in a way that would reject a conformant real harness.
func TestDecodeFrameLineAcceptsEveryPositiveFixture(t *testing.T) {
	dir := "../../docs/schema/fixtures/task-runtime-v1/positive"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	checked := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		// Fixtures are pretty-printed multi-line JSON; the wire format is
		// one line, so compact it first the same way a real harness's
		// json.Marshal output would already be.
		var v interface{}
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatalf("%s: unmarshal: %v", e.Name(), err)
		}
		compact, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: remarshal: %v", e.Name(), err)
		}
		fields, err := decodeFrameLine(compact)
		if err != nil {
			t.Errorf("%s: decodeFrameLine rejected a schema-valid fixture: %v", e.Name(), err)
			continue
		}
		if _, err := frameType(fields); err != nil {
			t.Errorf("%s: frameType: %v", e.Name(), err)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no positive fixtures found -- this test is running against nothing")
	}
}
