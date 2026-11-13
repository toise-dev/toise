package graphql

import (
	"context"

	gql "github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/ast"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/toise-dev/toise/internal/telemetry"
	"github.com/toise-dev/toise/internal/tenant"
)

const maxOperationNameLen = 64

// tracing is a gqlgen extension opening one span per GraphQL operation.
type tracing struct{}

// ExtensionName implements graphql.HandlerExtension.
func (tracing) ExtensionName() string { return "OpenTelemetryTracing" }

// Validate implements graphql.HandlerExtension.
func (tracing) Validate(gql.ExecutableSchema) error { return nil }

// InterceptResponse implements graphql.ResponseInterceptor.
func (tracing) InterceptResponse(ctx context.Context, next gql.ResponseHandler) *gql.Response {
	oc := gql.GetOperationContext(ctx)
	// Subscriptions live for hours; a span per subscription is useless.
	if oc.Operation != nil && oc.Operation.Operation == ast.Subscription {
		return next(ctx)
	}
	opType := "unknown"
	opName := "anonymous"
	if oc.Operation != nil {
		opType = string(oc.Operation.Operation)
		// The parser already validated the name: [_A-Za-z][_0-9A-Za-z]*.
		if oc.Operation.Name != "" {
			opName = oc.Operation.Name
			if len(opName) > maxOperationNameLen {
				opName = opName[:maxOperationNameLen]
			}
		}
	}
	attrs := []attribute.KeyValue{
		attribute.String("graphql.operation.name", opName),
		attribute.String("graphql.operation.type", opType),
		attribute.String(telemetry.AttrTenant, tenant.FromContext(ctx)),
	}
	ctx, span := telemetry.Tracer().Start(ctx, "graphql "+opType,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...),
	)
	defer span.End()
	resp := next(ctx)
	if resp != nil && len(resp.Errors) > 0 {
		span.SetStatus(codes.Error, resp.Errors[0].Message)
	}
	return resp
}
