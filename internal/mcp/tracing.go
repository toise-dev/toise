package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/toise-dev/toise/internal/telemetry"
	"github.com/toise-dev/toise/internal/tenant"
)

// knownTools must stay in sync with the tools registered on the server;
// TestKnownToolsMatchRegistered verifies this.
var knownTools = map[string]bool{
	"find_entities":   true,
	"get_entity":      true,
	"get_neighbors":   true,
	"find_path":       true,
	"impact_of":       true,
	"recent_changes":  true,
	"graph_diff":      true,
	"entity_history":  true,
	"describe_schema": true,
	"describe_type":   true,
	"telemetry_keys":  true,
	"annotate_entity": true,
}

// knownMethods is the bounded set of MCP protocol methods we trace.
var knownMethods = map[string]bool{
	"initialize":                true,
	"notifications/initialized": true,
	"ping":                      true,
	"tools/list":                true,
	"tools/call":                true,
	"prompts/list":              true,
	"prompts/get":               true,
	"resources/list":            true,
	"resources/read":            true,
	"resources/templates/list":  true,
	"completion/complete":       true,
	"logging/setLevel":          true,
}

// boundedMethod maps an arbitrary client-supplied method name to a known
// value, or "other", to keep span cardinality bounded.
func boundedMethod(m string) string {
	if knownMethods[m] {
		return m
	}
	return "other"
}

// boundedTool maps an arbitrary client-supplied tool name to a known value,
// or "unknown", to keep span cardinality bounded.
func boundedTool(n string) string {
	if knownTools[n] {
		return n
	}
	return "unknown"
}

// traceMiddleware opens one span per MCP method call.
func traceMiddleware(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		name := "mcp " + boundedMethod(method)
		attrs := []attribute.KeyValue{
			attribute.String("mcp.method.name", boundedMethod(method)),
			attribute.String(telemetry.AttrTenant, tenant.FromContext(ctx)),
		}
		if method == "tools/call" {
			if p, ok := req.GetParams().(*mcpsdk.CallToolParamsRaw); ok && p != nil {
				name = "mcp tools/call " + boundedTool(p.Name)
				attrs = append(attrs, attribute.String("mcp.tool.name", boundedTool(p.Name)))
			}
		}
		ctx, span := telemetry.Tracer().Start(ctx, name,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attrs...),
		)
		defer span.End()
		res, err := next(ctx, method, req)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		return res, err
	}
}
