package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

// TestInitDefaultIsNoop verifies that with no BROKOLI_OTEL_EXPORTER set
// (the default for every deployment that hasn't opted in), Init does not
// require a live OTLP collector, does not error, and Tracer().Start still
// works — go test ./... must never need a running collector to pass
// (Tnsor-Labs/brokoli#11).
func TestInitDefaultIsNoop(t *testing.T) {
	t.Setenv(EnvExporter, "")

	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init with no exporter configured should not error, got: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init should always return a non-nil shutdown func")
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			t.Errorf("no-op shutdown should not error, got: %v", err)
		}
	}()

	ctx, span := Tracer().Start(context.Background(), "test-span")
	if ctx == nil {
		t.Error("expected a non-nil context from Start")
	}
	span.End() // must not panic on the no-op provider
}

// TestInitUnknownExporterIsNoop verifies any value other than "otlp"
// (including typos) degrades to the same no-op behavior rather than
// erroring, since EnvExporter's contract is "only 'otlp' opts in."
func TestInitUnknownExporterIsNoop(t *testing.T) {
	t.Setenv(EnvExporter, "not-a-real-exporter")

	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatalf("unknown exporter value should degrade to no-op, not error: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("no-op shutdown should not error, got: %v", err)
	}
}

// TestInitOTLPConfiguresWithoutDialing verifies that BROKOLI_OTEL_EXPORTER=otlp
// succeeds at construction time without needing a live collector —
// otlptracehttp.New does not dial eagerly, it only configures the exporter;
// the actual export happens asynchronously via the batch span processor and
// simply fails silently (by design, OTel exporters must not crash the host
// application) if no collector is listening.
func TestInitOTLPConfiguresWithoutDialing(t *testing.T) {
	t.Setenv(EnvExporter, "otlp")
	t.Setenv(EnvEndpoint, "127.0.0.1:1") // deliberately unreachable
	t.Setenv(EnvInsecure, "true")
	t.Setenv(EnvServiceName, "brokoli-test")

	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init with otlp exporter should succeed even against an unreachable endpoint: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown against an unreachable collector should not error (best-effort flush): %v", err)
	}
}

var _ trace.Tracer = Tracer() // Tracer() must satisfy trace.Tracer at compile time
