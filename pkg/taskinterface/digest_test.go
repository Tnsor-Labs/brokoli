package taskinterface

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func decodeExact(t *testing.T, text string) interface{} {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return v
}

// The Go renderer gives exactly the text of every cross-SDK rendering
// vector (docs/schema/fixtures/ir-canonicalization/rendering.json), the
// same bytes the Python and TypeScript SDKs give.
func TestCanonicalRenderingMatchesTheCrossSDKVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "schema", "fixtures", "ir-canonicalization", "rendering.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []struct{ Name, Input, Canonical string } `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Cases {
		got, err := CanonicalRendering(decodeExact(t, c.Input))
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		if string(got) != c.Canonical {
			t.Errorf("%s: rendered %q, want %q", c.Name, got, c.Canonical)
		}
	}
}

func TestCanonicalRenderingRefusesWhatHasNoPortableForm(t *testing.T) {
	if _, err := CanonicalRendering(map[string]interface{}{"x": string([]byte{0xff})}); err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Errorf("invalid UTF-8: %v", err)
	}
	for _, v := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
		if _, err := CanonicalRendering(v); err == nil {
			t.Errorf("%v rendered", v)
		}
	}
	if _, err := CanonicalRendering(struct{}{}); err == nil {
		t.Error("a struct rendered")
	}
}

// Record fields are sorted by name before hashing; every other list keeps
// its order, an enum's values included.
func TestDigestSortsRecordFieldsOnly(t *testing.T) {
	record := func(names ...string) map[string]interface{} {
		var fields []interface{}
		for _, n := range names {
			fields = append(fields, map[string]interface{}{"name": n, "type": map[string]interface{}{"kind": "int64"}})
		}
		return map[string]interface{}{"kind": "record", "fields": fields}
	}
	a, _ := Digest(record("b", "a"))
	b, _ := Digest(record("a", "b"))
	if a != b {
		t.Error("field declaration order changed the digest")
	}
	e1, _ := Digest(map[string]interface{}{"kind": "enum", "values": []interface{}{"gold", "silver"}})
	e2, _ := Digest(map[string]interface{}{"kind": "enum", "values": []interface{}{"silver", "gold"}})
	if e1 == e2 {
		t.Error("enum value order did not change the digest")
	}
	in := record("b", "a")
	if _, err := Digest(in); err != nil {
		t.Fatal(err)
	}
	if in["fields"].([]interface{})[0].(map[string]interface{})["name"] != "b" {
		t.Error("Digest reordered the caller's document")
	}
}

// The values the JVM SDK computed for brokoli#990 and the Python SDK's
// renderer reproduced, now from Go: the three agree.
func TestInterfaceDigestAgreesWithTheJVMAndPythonSDKs(t *testing.T) {
	got, err := InterfaceDigest(nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "sha256:2897112391937ea03be0faaf630b6d082e1c192484a174fe32ed523d440abfa2"; got != want {
		t.Errorf("unknown-rows interface digest = %s, want %s", got, want)
	}
	rendered, _ := CanonicalRendering(UnknownRowsInterface())
	if len(rendered) != 342 || !bytes.HasSuffix(rendered, []byte("}\n")) {
		t.Errorf("unknown-rows rendering is %d bytes, want 342", len(rendered))
	}
}

// Every cross-SDK differential vector carries the exact rendering and
// digest of its interface (the unknown-rows interface when the expected
// interface is null) and, for a parameter vector, of its parameters. Core
// computes the same values; each SDK compares its own with these fields.
func TestDifferentialVectorsCarryTheirDigests(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "schema", "fixtures", "task-interface", "differential")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		v := decodeExact(t, string(raw)).(map[string]interface{})
		check := func(field string, doc interface{}, digest func(interface{}) (string, error)) {
			canonical, _ := v["expected_"+field+"_canonical"].(string)
			want, _ := v["expected_"+field+"_digest"].(string)
			if canonical == "" || want == "" {
				t.Errorf("%s: missing expected_%s_canonical or expected_%s_digest", e.Name(), field, field)
				return
			}
			rendered, err := CanonicalRendering(doc)
			if err != nil {
				t.Errorf("%s: %v", e.Name(), err)
				return
			}
			if string(rendered) != canonical {
				t.Errorf("%s: %s rendering\n got %q\nwant %q", e.Name(), field, rendered, canonical)
			}
			if got, _ := digest(doc); got != want {
				t.Errorf("%s: %s digest %s, want %s", e.Name(), field, got, want)
			}
		}
		iface := v["expected_node_interface"]
		check("interface", orUnknownRows(iface), InterfaceDigest)
		if params := v["expected_pipeline_parameters"]; params != nil {
			check("parameters", params, Digest)
		} else if _, ok := v["expected_parameters_digest"]; ok {
			t.Errorf("%s: a parameters digest without parameters", e.Name())
		}
		checked++
	}
	if checked < 6 {
		t.Fatalf("checked %d vectors, want at least 6", checked)
	}
}

func orUnknownRows(v interface{}) interface{} {
	if v == nil {
		return UnknownRowsInterface()
	}
	return v
}

// Characters Go's encoding/json escapes but JSON.stringify and Python's
// json.dumps(ensure_ascii=False) write literally stay literal.
func TestCanonicalRenderingWritesWhatGoWouldEscapeLiterally(t *testing.T) {
	s := "<a&b>" + string(rune(0x2028)) + string(rune(0x2029)) + string(rune(0x7f))
	got, err := CanonicalRendering(s)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"` + s + `"` + "\n"; string(got) != want {
		t.Errorf("rendered %q, want %q", got, want)
	}
}
