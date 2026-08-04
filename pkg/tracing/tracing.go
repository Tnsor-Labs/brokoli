// Package tracing wires OpenTelemetry distributed tracing into Brokoli's
// run/attempt lifecycle (Tnsor-Labs/brokoli#11).
//
// It is additive and off by default: Init returns a no-op shutdown and
// leaves the global otel.TracerProvider untouched (which itself defaults to
// a no-op implementation — every span created via Tracer().Start(...)
// anywhere in the codebase is a free, zero-allocation-ish no-op) unless an
// operator explicitly opts in via BROKOLI_OTEL_EXPORTER=otlp. Deployments
// that never set that env var see zero behavior change and pay no runtime
// cost for a collector they haven't configured; go test ./... never needs a
// live OTLP collector for the same reason — the default provider in tests
// is always the no-op one.
package tracing

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

// instrumentationName is the tracer name every span in this codebase is
// created under — see Tracer().
const instrumentationName = "github.com/Tnsor-Labs/brokoli"

// Env vars controlling the exporter. All optional; absence (or any value
// for BROKOLI_OTEL_EXPORTER other than "otlp") means tracing stays a no-op.
const (
	// EnvExporter selects the exporter. Only "otlp" currently enables real
	// export; anything else (including unset) is a no-op.
	EnvExporter = "BROKOLI_OTEL_EXPORTER"
	// EnvEndpoint is the OTLP/HTTP collector endpoint, host:port form (no
	// scheme), e.g. "localhost:4318". Defaults to
	// otlptracehttp's own default (localhost:4318) when unset.
	EnvEndpoint = "BROKOLI_OTEL_ENDPOINT"
	// EnvInsecure, when "true", disables TLS for the OTLP/HTTP exporter
	// (typical for a local/sidecar collector). Defaults to insecure=true
	// since the common case is a same-host or same-cluster collector;
	// set to "false" to require TLS against a remote collector.
	EnvInsecure = "BROKOLI_OTEL_INSECURE"
	// EnvServiceName overrides the resource's service.name attribute.
	EnvServiceName = "BROKOLI_OTEL_SERVICE_NAME"
)

const defaultServiceName = "brokoli"

// Shutdown flushes and stops the tracer provider. Safe to call the no-op
// value returned when tracing is disabled.
type Shutdown func(context.Context) error

// Init configures the global OpenTelemetry TracerProvider from environment
// variables (see Env* constants above) and returns a Shutdown func the
// caller must defer. When BROKOLI_OTEL_EXPORTER is not "otlp", Init does
// nothing and returns a no-op Shutdown — this is the default for every
// deployment that hasn't explicitly configured a collector.
func Init(ctx context.Context) (Shutdown, error) {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv(EnvExporter)), "otlp") {
		return func(context.Context) error { return nil }, nil
	}

	opts := []otlptracehttp.Option{}
	if endpoint := os.Getenv(EnvEndpoint); endpoint != "" {
		opts = append(opts, otlptracehttp.WithEndpoint(endpoint))
	}
	insecure := true
	if v := os.Getenv(EnvInsecure); v != "" {
		insecure = strings.EqualFold(v, "true")
	}
	if insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("tracing: create otlp exporter: %w", err)
	}

	serviceName := os.Getenv(EnvServiceName)
	if serviceName == "" {
		serviceName = defaultServiceName
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return nil, fmt.Errorf("tracing: build resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	shutdown := func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return tp.Shutdown(ctx)
	}
	return shutdown, nil
}

// Tracer returns the tracer every span in this codebase should be created
// from. Before Init is called (or when tracing is disabled), this resolves
// to OpenTelemetry's default no-op TracerProvider, so calling Start on the
// returned Tracer is always safe.
func Tracer() trace.Tracer {
	return otel.Tracer(instrumentationName)
}
