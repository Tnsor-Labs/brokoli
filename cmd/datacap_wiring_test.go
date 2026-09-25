package cmd

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/engine"
)

// The bug this guards: the issuer was wired only on the API/all path, so
// a --mode worker process -- the one that dispatches task nodes in a
// distributed deployment -- had none. A task input over the inline row
// cap was refused with "this server issues no data capabilities" instead
// of being staged by reference.
//
// The reference-based input path shipped with a green preflight and full
// CI and still could not run in the only mode that needed it, because
// nothing asserted the wiring.
func TestDataCapIssuerIsWired(t *testing.T) {
	eng := &engine.Engine{}
	if eng.DataCapIssuer != nil {
		t.Fatal("precondition: a fresh engine should have no issuer")
	}
	wireDataCapIssuer(eng)
	if eng.DataCapIssuer == nil {
		t.Fatal("wireDataCapIssuer left the engine without an issuer; a task input over the inline cap would be refused rather than staged")
	}
}

// The issuer the engine mints with and the one the blob endpoint
// verifies against must be the SAME object. Two issuers derived
// separately would still both work in isolation and fail only when a
// worker presented a real capability, which is the worst place to find
// out.
func TestEngineAndBlobEndpointShareOneIssuer(t *testing.T) {
	eng := &engine.Engine{}
	wireDataCapIssuer(eng)

	fromAPI, err := apiDatacapIssuerForTest()
	if err != nil {
		t.Fatalf("api issuer: %v", err)
	}
	if eng.DataCapIssuer != fromAPI {
		t.Error("the engine mints with a different issuer than the blob endpoint verifies with; nothing it issues would verify")
	}
}
