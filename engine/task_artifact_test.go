package engine

// ADR-032 section 6: "Artifacts may constrain media_types."
//
// Tested directly rather than through a pipeline: the engine hands the
// reference harnesses the port's own first allowed media type, so a
// well-behaved one can never label an artifact outside the set. The
// check exists for a producer the engine did not label -- ADR-033
// section 3 admits `native` payloads that speak the protocol directly,
// and a harness's declarations are claims, not facts, exactly like the
// size and checksum the worker re-verifies. An end-to-end test here
// would skip forever and read as coverage that does not exist.

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/taskinterface"
)

func TestArtifactMediaType_UnconstrainedPortAcceptsWhatTheProducerDeclared(t *testing.T) {
	got, err := artifactMediaType("image/png", taskinterface.PortValue{Kind: taskinterface.ValueArtifact})
	if err != nil {
		t.Fatalf("unconstrained port rejected a declared media type: %v", err)
	}
	if got != "image/png" {
		t.Errorf("media type = %q, want image/png", got)
	}
}

func TestArtifactMediaType_AbsentDeclarationFallsBackToOpaque(t *testing.T) {
	got, err := artifactMediaType("", taskinterface.PortValue{Kind: taskinterface.ValueArtifact})
	if err != nil {
		t.Fatal(err)
	}
	if got != "application/octet-stream" {
		t.Errorf("media type = %q, want the opaque default", got)
	}
}

func TestArtifactMediaType_ConstrainedPortRefusesAnythingOutsideTheSet(t *testing.T) {
	port := taskinterface.PortValue{Kind: taskinterface.ValueArtifact, MediaTypes: []string{"application/pdf"}}
	_, err := artifactMediaType("image/png", port)
	if err == nil {
		t.Fatal("a media type outside the declared set was accepted")
	}
	if !strings.Contains(err.Error(), "image/png") || !strings.Contains(err.Error(), "application/pdf") {
		t.Errorf("err = %v, want both the offending and the allowed types named", err)
	}
}

func TestArtifactMediaType_ConstrainedPortAcceptsAMemberOfTheSet(t *testing.T) {
	port := taskinterface.PortValue{Kind: taskinterface.ValueArtifact, MediaTypes: []string{"application/pdf", "image/png"}}
	got, err := artifactMediaType("image/png", port)
	if err != nil {
		t.Fatalf("a declared media type in the set was rejected: %v", err)
	}
	if got != "image/png" {
		t.Errorf("media type = %q, want image/png", got)
	}
}
