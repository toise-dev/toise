package telemetry

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSetupNoopWithoutEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_SDK_DISABLED", "")
	shutdown, err := Setup(context.Background(), testLogger())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if shutdown == nil {
		t.Fatal("shutdown must not be nil")
	}
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); ok {
		t.Fatal("global provider must stay no-op without an endpoint")
	}
}

func TestSetupEnabledWithEndpoint(t *testing.T) {
	orig := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(orig) })

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_SDK_DISABLED", "")
	shutdown, err := Setup(context.Background(), testLogger())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Fatal("global provider must be a *sdktrace.TracerProvider with an endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Export to an unreachable endpoint may fail; shutdown must not block.
	_ = shutdown(ctx)
}

func TestSetupDisabledBySDKFlag(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_SDK_DISABLED", "true")
	shutdown, err := Setup(context.Background(), testLogger())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if shutdown == nil {
		t.Fatal("shutdown must not be nil")
	}
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); ok {
		t.Fatal("OTEL_SDK_DISABLED=true must keep the no-op provider")
	}
}

func TestNewExporterProtocolSelection(t *testing.T) {
	tests := []struct {
		name           string
		tracesProtocol string
		genericProto   string
		wantProtocol   string
		wantErr        string
	}{
		{
			name:         "default is grpc",
			wantProtocol: "grpc",
		},
		{
			name:         "explicit grpc",
			genericProto: "grpc",
			wantProtocol: "grpc",
		},
		{
			name:         "http/protobuf",
			genericProto: "http/protobuf",
			wantProtocol: "http/protobuf",
		},
		{
			name:           "traces protocol wins over generic",
			tracesProtocol: "http/protobuf",
			genericProto:   "grpc",
			wantProtocol:   "http/protobuf",
		},
		{
			name:         "http/json rejected",
			genericProto: "http/json",
			wantErr:      "http/json",
		},
		{
			name:         "thrift rejected",
			genericProto: "thrift",
			wantErr:      "thrift",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", tt.tracesProtocol)
			t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", tt.genericProto)
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
			exp, protocol, err := newExporter(context.Background())
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("newExporter must fail for protocol %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q must name the rejected value %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("newExporter: %v", err)
			}
			if protocol != tt.wantProtocol {
				t.Fatalf("protocol = %q, want %q", protocol, tt.wantProtocol)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = exp.Shutdown(ctx)
		})
	}
}

func TestSetupInvalidProtocol(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/json")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "")
	t.Setenv("OTEL_SDK_DISABLED", "")
	shutdown, err := Setup(context.Background(), testLogger())
	if err == nil {
		t.Fatal("Setup must fail on an unsupported protocol")
	}
	if !strings.Contains(err.Error(), "http/json") {
		t.Fatalf("error %q must name the rejected value", err)
	}
	if shutdown != nil {
		t.Fatal("shutdown must be nil on error")
	}
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); ok {
		t.Fatal("global provider must stay no-op when Setup fails")
	}
}

func TestSetupHTTPProtobufReachesHTTPEndpoint(t *testing.T) {
	type received struct {
		method string
		path   string
	}
	reqs := make(chan received, 16)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs <- received{method: r.Method, path: r.URL.Path}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_SDK_DISABLED", "")

	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	prevProp := otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTextMapPropagator(prevProp) })

	shutdown, err := Setup(context.Background(), testLogger())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	_, span := Tracer().Start(context.Background(), "test-span")
	span.End()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	deadline := time.After(5 * time.Second)
	var got []string
	for {
		select {
		case r := <-reqs:
			got = append(got, r.method+" "+r.path)
			if r.method == http.MethodPost && r.path == "/v1/traces" {
				return
			}
		case <-deadline:
			t.Fatalf("no POST /v1/traces received; got %v", got)
		}
	}
}
