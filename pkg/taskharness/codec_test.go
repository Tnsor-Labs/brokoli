package taskharness

import (
	"strings"
	"testing"
)

func TestDecodeFrameLineRejectsInvalidUTF8(t *testing.T) {
	_, err := decodeFrameLine([]byte("{\"type\":\"log\",\"level\":\"info\",\"message\":\"\xff\xfe\"}"))
	if err == nil {
		t.Fatal("expected invalid UTF-8 to be rejected")
	}
}

func TestDecodeFrameLineRejectsEmptyLine(t *testing.T) {
	if _, err := decodeFrameLine(nil); err == nil {
		t.Fatal("expected an empty line to be rejected")
	}
}

func TestDecodeFrameLineRejectsNonObject(t *testing.T) {
	if _, err := decodeFrameLine([]byte(`["not", "an", "object"]`)); err == nil {
		t.Fatal("expected a non-object frame to be rejected")
	}
}

func TestDecodeFrameLineRejectsTrailingContent(t *testing.T) {
	if _, err := decodeFrameLine([]byte(`{"type":"completed"} {"type":"completed"}`)); err == nil {
		t.Fatal("expected trailing content after the JSON object to be rejected")
	}
}

func TestDecodeFrameLineRejectsMalformedKey(t *testing.T) {
	if _, err := decodeFrameLine([]byte(`{"type"`)); err == nil {
		t.Fatal("expected a truncated object to be rejected")
	}
}

func TestFrameTypeRequiresTypeField(t *testing.T) {
	fields, err := decodeFrameLine([]byte(`{"foo":"bar"}`))
	if err != nil {
		t.Fatalf("decodeFrameLine: %v", err)
	}
	if _, err := frameType(fields); err == nil {
		t.Fatal("expected a missing \"type\" field to be rejected")
	}
}

func TestEncodeFrameRejectsOversizedFrame(t *testing.T) {
	huge := Log{Level: "info", Message: strings.Repeat("x", MaxFrameLine+1)}
	if _, err := encodeFrame(huge); err == nil {
		t.Fatal("expected an oversized frame to be rejected")
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	original := Ready{Adapter: "pyharness", AdapterVersion: "0.1.0", Capabilities: []string{"python-adapter-1.0+"}}
	frame := struct {
		Type string `json:"type"`
		Ready
	}{Type: "ready", Ready: original}
	b, err := encodeFrame(frame)
	if err != nil {
		t.Fatalf("encodeFrame: %v", err)
	}
	fields, err := decodeFrameLine(bytesTrimLF(b))
	if err != nil {
		t.Fatalf("decodeFrameLine: %v", err)
	}
	typ, err := frameType(fields)
	if err != nil || typ != "ready" {
		t.Fatalf("frameType = %q, %v", typ, err)
	}
	var got Ready
	if err := decodeInto(fields, &got); err != nil {
		t.Fatalf("decodeInto: %v", err)
	}
	if got.Adapter != original.Adapter || len(got.Capabilities) != 1 {
		t.Errorf("round-tripped Ready = %+v, want %+v", got, original)
	}
}
