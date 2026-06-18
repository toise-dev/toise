package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/toise-dev/toise/internal/annotations"
	"github.com/toise-dev/toise/internal/auth"
)

// ctxWith runs the real auth middleware with the given token and returns the
// request context it tags, so a handler test exercises the production
// write-capability decision rather than a fabricated flag.
func ctxWith(t *testing.T, a *auth.Authenticator, token string) context.Context {
	t.Helper()
	var got context.Context
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Context() })
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	a.HTTPMiddleware(nil)(next).ServeHTTP(httptest.NewRecorder(), req)
	if got == nil {
		t.Fatalf("middleware did not serve token %q", token)
	}
	return got
}

func TestAnnotateEntity(t *testing.T) {
	s := newTestServer()
	dir := t.TempDir()
	store, err := annotations.Open(dir)
	if err != nil {
		t.Fatalf("open annotations: %v", err)
	}
	defer func() { _ = store.Close() }()
	s.SetAnnotations(store)

	ctx := context.Background() // auth disabled => writable

	// Set then surface on get_entity.
	_, out, err := s.annotateEntity(ctx, nil, AnnotateEntityInput{
		EntityID:    "01HOST_WEB",
		Annotations: map[string]string{"owner": "team-sre", "ticket": "OPS-1"},
	})
	if err != nil {
		t.Fatalf("annotateEntity: %v", err)
	}
	if out.Annotation.Values["owner"] != "team-sre" || out.Annotation.Values["ticket"] != "OPS-1" {
		t.Fatalf("unexpected merged annotation: %+v", out.Annotation)
	}

	_, ge, err := s.getEntity(ctx, nil, GetEntityInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("getEntity: %v", err)
	}
	if ge.Annotations == nil || ge.Annotations.Values["owner"] != "team-sre" {
		t.Fatalf("annotation not surfaced on get_entity: %+v", ge.Annotations)
	}

	// An empty value removes a key.
	if _, _, err := s.annotateEntity(ctx, nil, AnnotateEntityInput{
		EntityID:    "01HOST_WEB",
		Annotations: map[string]string{"ticket": ""},
	}); err != nil {
		t.Fatalf("annotate remove: %v", err)
	}
	_, ge, _ = s.getEntity(ctx, nil, GetEntityInput{EntityID: "01HOST_WEB"})
	if _, ok := ge.Annotations.Values["ticket"]; ok {
		t.Errorf("empty value should have removed key, got %+v", ge.Annotations.Values)
	}

	// Validation: unknown entity, missing id, no annotations.
	if _, _, err := s.annotateEntity(ctx, nil, AnnotateEntityInput{EntityID: "nope", Annotations: map[string]string{"a": "b"}}); err == nil {
		t.Error("annotating an unknown entity must error")
	}
	if _, _, err := s.annotateEntity(ctx, nil, AnnotateEntityInput{Annotations: map[string]string{"a": "b"}}); err == nil {
		t.Error("missing entity_id must error")
	}
	if _, _, err := s.annotateEntity(ctx, nil, AnnotateEntityInput{EntityID: "01HOST_WEB"}); err == nil {
		t.Error("empty annotations must error")
	}
}

// TestAnnotateEntityWriteGate proves a read-only token cannot annotate while a
// full token can — using the real auth middleware to tag the request context.
func TestAnnotateEntityWriteGate(t *testing.T) {
	s := newTestServer()
	store, err := annotations.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open annotations: %v", err)
	}
	defer func() { _ = store.Close() }()
	s.SetAnnotations(store)

	a := auth.NewWithRoles([]string{"full"}, []string{"reader"}, nil, nil)
	in := AnnotateEntityInput{EntityID: "01HOST_WEB", Annotations: map[string]string{"owner": "sre"}}

	if _, _, err := s.annotateEntity(ctxWith(t, a, "reader"), nil, in); err == nil {
		t.Error("a read-only token must not be allowed to annotate")
	}
	if _, _, err := s.annotateEntity(ctxWith(t, a, "full"), nil, in); err != nil {
		t.Errorf("a full token must be allowed to annotate: %v", err)
	}
}

// TestAnnotateEntityDisabled: with no sidecar wired, annotate errors and
// get_entity simply carries no annotation.
func TestAnnotateEntityDisabled(t *testing.T) {
	s := newTestServer()
	if _, _, err := s.annotateEntity(context.Background(), nil, AnnotateEntityInput{
		EntityID: "01HOST_WEB", Annotations: map[string]string{"a": "b"},
	}); err == nil {
		t.Error("annotate with no sidecar must error")
	}
	_, ge, err := s.getEntity(context.Background(), nil, GetEntityInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("getEntity: %v", err)
	}
	if ge.Annotations != nil {
		t.Errorf("no sidecar => no annotation, got %+v", ge.Annotations)
	}
}
