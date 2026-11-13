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
	name := oc.OperationName
	if name == "" && oc.Operation != nil {
		name = oc.Operation.Name // a client may name the operation in the document only
	}
	if name == "" {
		name = "anonymous"
	}
	attrs := []attribute.KeyValue{
		attribute.String("graphql.operation.name", name),
		attribute.String(telemetry.AttrTenant, tenant.FromContext(ctx)),
	}
	if oc.Operation != nil {
		attrs = append(attrs, attribute.String("graphql.operation.type", string(oc.Operation.Operation)))
	}
	ctx, span := telemetry.Tracer().Start(ctx, "graphql "+name,
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
