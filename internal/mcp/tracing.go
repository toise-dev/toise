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

// traceMiddleware opens one span per MCP method call.
func traceMiddleware(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		name := "mcp " + method
		attrs := []attribute.KeyValue{
			attribute.String("mcp.method.name", method),
			attribute.String(telemetry.AttrTenant, tenant.FromContext(ctx)),
		}
		if method == "tools/call" {
			if p, ok := req.GetParams().(*mcpsdk.CallToolParamsRaw); ok && p != nil {
				name = "mcp tools/call " + p.Name
				attrs = append(attrs, attribute.String("mcp.tool.name", p.Name))
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
