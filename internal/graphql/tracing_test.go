package graphql_test

import (
	"testing"

	"github.com/99designs/gqlgen/client"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTracingSpanPerOperation(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	orig := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(orig) })

	s := newStack(t)
	var resp struct {
		Entity struct {
			ID string
		}
	}
	s.client(t).MustPost(`query Probe($id:ID!){ entity(id:$id){ id } }`, &resp, client.Var("id", string(s.hostID)))

	found := false
	for _, span := range rec.Ended() {
		if span.Name() == "graphql Probe" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no ended span named %q; got %v", "graphql Probe", rec.Ended())
	}
}
