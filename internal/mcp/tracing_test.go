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

func TestTraceMiddlewareUnknownToolName(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	orig := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(orig) })

	next := func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		return nil, nil
	}
	req := &mcpsdk.ServerRequest[*mcpsdk.CallToolParamsRaw]{Params: &mcpsdk.CallToolParamsRaw{Name: "drop table; <x>"}}
	if _, err := traceMiddleware(next)(context.Background(), "tools/call", req); err != nil {
		t.Fatalf("next: %v", err)
	}

	found := false
	for _, span := range rec.Ended() {
		if span.Name() == "mcp tools/call unknown" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no ended span named %q; got %v", "mcp tools/call unknown", rec.Ended())
	}
}

func TestTraceMiddlewareUnknownMethod(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	orig := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(orig) })

	next := func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		return nil, nil
	}
	req := &mcpsdk.ServerRequest[*mcpsdk.PingParams]{}
	if _, err := traceMiddleware(next)(context.Background(), "evil/method", req); err != nil {
		t.Fatalf("next: %v", err)
	}

	found := false
	for _, span := range rec.Ended() {
		if span.Name() == "mcp other" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no ended span named %q; got %v", "mcp other", rec.Ended())
	}
}

func TestKnownToolsMatchRegistered(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	clientT, serverT := mcpsdk.NewInMemoryTransports()
	ss, err := s.srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = ss.Wait() }()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = cs.Close() }()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) != len(knownTools) {
		t.Fatalf("registered %d tools, knownTools has %d", len(tools.Tools), len(knownTools))
	}
	for _, tool := range tools.Tools {
		if !knownTools[tool.Name] {
			t.Errorf("registered tool %q is missing from knownTools", tool.Name)
		}
	}
}
