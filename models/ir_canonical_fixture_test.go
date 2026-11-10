package models

// The cross-SDK rendering vectors (docs/schema/fixtures/ir-canonicalization)
// are only useful if each one is honest: its expected rendering must be the
// same JSON value as its input, so the SDKs are held to a layout, never to a
// different value. Core renders no IR itself, so this checks the fixture,
// not a renderer.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIRCanonicalRenderingVectorsAreTheirInputsValues(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "docs", "schema", "fixtures", "ir-canonicalization", "rendering.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []struct {
			Name      string `json:"name"`
			Input     string `json:"input"`
			Canonical string `json:"canonical"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) < 30 {
		t.Fatalf("only %d cases; the fixture lost its vectors", len(doc.Cases))
	}
	names := map[string]bool{}
	for _, c := range doc.Cases {
		if names[c.Name] {
			t.Errorf("duplicate case name %q", c.Name)
		}
		names[c.Name] = true
		if !strings.HasSuffix(c.Canonical, "\n") || strings.HasSuffix(c.Canonical, "\n\n") {
			t.Errorf("%s: canonical text must end in exactly one newline", c.Name)
		}
		var in, out interface{}
		if err := json.Unmarshal([]byte(c.Input), &in); err != nil {
			t.Errorf("%s: input is not JSON: %v", c.Name, err)
			continue
		}
		if err := json.Unmarshal([]byte(c.Canonical), &out); err != nil {
			t.Errorf("%s: canonical is not JSON: %v", c.Name, err)
			continue
		}
		if !reflect.DeepEqual(in, out) {
			t.Errorf("%s: canonical %q is a different value from input %q", c.Name, c.Canonical, c.Input)
		}
	}
}
