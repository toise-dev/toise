// Package telemetry wires OpenTelemetry tracing into Toise. Tracing stays off
// unless an OTLP endpoint is configured, so a default deployment pays nothing
// for it (no-op provider, no connection).
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/toise-dev/toise/internal/version"
)

// AttrTenant is the span attribute carrying the tenant id.
const AttrTenant = "toise.tenant"

// Setup installs a global OTLP tracer provider when an OTLP endpoint is set;
// otherwise it returns a no-op shutdown and leaves the global provider alone.
func Setup(ctx context.Context, logger *slog.Logger) (shutdown func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }
	if os.Getenv("OTEL_SDK_DISABLED") == "true" ||
		(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "") {
		return noop, nil
	}
	exp, protocol, err := newExporter(ctx)
	if err != nil {
		return nil, err
	}
	// WithFromEnv last so OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES can
	// override the explicit service.name.
	res, err := resource.New(ctx,
		resource.WithHost(),
		resource.WithAttributes(
			attribute.String("service.name", "toise"),
			attribute.String("service.version", version.Version),
		),
		resource.WithFromEnv(),
	)
	if err != nil {
		if !errors.Is(err, resource.ErrPartialResource) {
			return nil, err
		}
		logger.Warn("partial resource attributes", "error", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	logger.Info("tracing enabled", "exporter", "otlp/"+protocol)
	return tp.Shutdown, nil
}

// newExporter builds the OTLP span exporter selected by
// OTEL_EXPORTER_OTLP_TRACES_PROTOCOL, falling back to OTEL_EXPORTER_OTLP_PROTOCOL
// and defaulting to gRPC. It returns the protocol name that was used.
func newExporter(ctx context.Context) (sdktrace.SpanExporter, string, error) {
	variable := "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"
	protocol := os.Getenv(variable)
	if protocol == "" {
		variable = "OTEL_EXPORTER_OTLP_PROTOCOL"
		protocol = os.Getenv(variable)
	}
	switch protocol {
	case "", "grpc":
		exp, err := otlptracegrpc.New(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("create otlptracegrpc exporter: %w", err)
		}
		return exp, "grpc", nil
	case "http/protobuf":
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("create otlptracehttp exporter: %w", err)
		}
		return exp, "http/protobuf", nil
	default:
		return nil, "", fmt.Errorf("unsupported %s %q: accepted values are \"grpc\" and \"http/protobuf\"", variable, protocol)
	}
}

// Tracer returns the Toise tracer.
func Tracer() trace.Tracer {
	return otel.Tracer("github.com/toise-dev/toise")
}
