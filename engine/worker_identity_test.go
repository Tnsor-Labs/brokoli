package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
)

// The bug: the capability store was built with no AuthHeader, so a
// worker presented only its capability. The blob endpoint requires a
// caller identity too and rejected the request with 401 before the
// capability was ever examined -- every reference-based input fetch
// failed on a real fleet while every in-process test passed, because an
// in-process worker never reaches the endpoint.
func TestOutputStoreCarriesTheWorkerIdentity(t *testing.T) {
	prev := WorkerDataPlaneAuth
	t.Cleanup(func() { WorkerDataPlaneAuth = prev })
	WorkerDataPlaneAuth = func() string { return "Bearer brk_wp_test" }

	s := workOrderOutputStore(&extensions.InstanceWorkOrder{
		OutputCapability: "cap", ControlPlaneURL: "https://brokoli.example",
	}, "run-1", "task-1")
	cs, ok := s.(*artifact.CapabilityStore)
	if !ok {
		t.Fatalf("store is %T", s)
	}
	if cs.AuthHeader != "Bearer brk_wp_test" {
		t.Errorf("AuthHeader = %q; without it the endpoint rejects the request before reading the capability", cs.AuthHeader)
	}
	// Identity and grant are separate things and must both travel.
	if cs.WriteCapability == "" {
		t.Error("the capability went missing while adding the identity")
	}
}

// No hook and no env means no identity, and that must stay a visible
// 401 from the endpoint rather than a fabricated credential.
func TestNoConfiguredIdentitySendsNone(t *testing.T) {
	prev := WorkerDataPlaneAuth
	t.Cleanup(func() { WorkerDataPlaneAuth = prev })
	WorkerDataPlaneAuth = nil
	t.Setenv("BROKOLI_WORKER_TOKEN", "")

	if got := workerAuthHeader(); got != "" {
		t.Errorf("workerAuthHeader() = %q, want empty", got)
	}
}

// A core-only deployment configures the token by environment; the
// enterprise hook wins when both are present, because a pool token is
// the more specific identity.
func TestEnvFallbackAndHookPrecedence(t *testing.T) {
	prev := WorkerDataPlaneAuth
	t.Cleanup(func() { WorkerDataPlaneAuth = prev })

	WorkerDataPlaneAuth = nil
	t.Setenv("BROKOLI_WORKER_TOKEN", "brk_env")
	if got := workerAuthHeader(); got != "Bearer brk_env" {
		t.Errorf("env fallback = %q", got)
	}

	WorkerDataPlaneAuth = func() string { return "Bearer brk_wp_hook" }
	if got := workerAuthHeader(); got != "Bearer brk_wp_hook" {
		t.Errorf("hook should win over env, got %q", got)
	}

	// A hook that returns empty falls through rather than silently
	// suppressing a configured env token.
	WorkerDataPlaneAuth = func() string { return "" }
	if got := workerAuthHeader(); got != "Bearer brk_env" {
		t.Errorf("empty hook should fall through to env, got %q", got)
	}
}
