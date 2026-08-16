package plugins

import "testing"

// TestManifest_Validate_SupportsPlanRequiresSource guards ADR-013 M3's
// scoping decision: `plan` only makes sense for a source (you plan how
// to *read* a stream in pieces), so a manifest declaring supports_plan
// on a sink or transform must fail Validate rather than loading
// successfully and silently doing nothing.
func TestManifest_Validate_SupportsPlanRequiresSource(t *testing.T) {
	base := func(kind NodeKind, supportsPlan bool) Manifest {
		return Manifest{
			ProtocolVersion: 1,
			Name:            "test",
			Version:         "0.1.0",
			Binary:          "./bin",
			NodeTypes: []NodeTypeDecl{
				{Type: "node_test", Kind: kind, SupportsPlan: supportsPlan},
			},
		}
	}

	t.Run("source with supports_plan is valid", func(t *testing.T) {
		m := base(KindSource, true)
		if err := m.Validate(); err != nil {
			t.Fatalf("Validate: unexpected error: %v", err)
		}
	})

	t.Run("sink with supports_plan is rejected", func(t *testing.T) {
		m := base(KindSink, true)
		if err := m.Validate(); err == nil {
			t.Fatal("Validate: expected an error for supports_plan on a sink, got nil")
		}
	})

	t.Run("transform with supports_plan is rejected", func(t *testing.T) {
		m := base(KindTransform, true)
		if err := m.Validate(); err == nil {
			t.Fatal("Validate: expected an error for supports_plan on a transform, got nil")
		}
	})

	t.Run("source without supports_plan is valid, unaffected", func(t *testing.T) {
		m := base(KindSource, false)
		if err := m.Validate(); err != nil {
			t.Fatalf("Validate: unexpected error: %v", err)
		}
	})
}
