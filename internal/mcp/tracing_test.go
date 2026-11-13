package mcp

import (
	"context"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTraceMiddlewareToolCall(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	orig := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(orig) })

	next := func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		return nil, nil
	}
	req := &mcpsdk.ServerRequest[*mcpsdk.CallToolParamsRaw]{Params: &mcpsdk.CallToolParamsRaw{Name: "get_entity"}}
	if _, err := traceMiddleware(next)(context.Background(), "tools/call", req); err != nil {
		t.Fatalf("next: %v", err)
	}

	found := false
	for _, span := range rec.Ended() {
		if span.Name() != "mcp tools/call get_entity" {
			continue
		}
		found = true
		hasTool := false
		for _, a := range span.Attributes() {
			if a.Key == attribute.Key("mcp.tool.name") && a.Value.AsString() == "get_entity" {
				hasTool = true
			}
		}
		if !hasTool {
			t.Fatalf("span missing mcp.tool.name attribute: %v", span.Attributes())
		}
	}
	if !found {
		t.Fatalf("no ended span named %q; got %v", "mcp tools/call get_entity", rec.Ended())
	}
}
