package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/toise-dev/toise/internal/annotations"
	"github.com/toise-dev/toise/internal/audit"
	"github.com/toise-dev/toise/internal/auth"
)

// tokenAuthor returns the principal the auth middleware derives for a token.
func tokenAuthor(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "token:" + hex.EncodeToString(sum[:])[:8]
}

func TestAnnotateEntityRecordsTokenAuthor(t *testing.T) {
	s := newTestServer()
	store, err := annotations.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open annotations: %v", err)
	}
	defer func() { _ = store.Close() }()
	s.SetAnnotations(store)

	a := auth.NewWithRoles([]string{"full"}, nil, nil, nil)
	ctx := ctxWith(t, a, "full")

	_, out, err := s.annotateEntity(ctx, nil, AnnotateEntityInput{
		EntityID:    "01HOST_WEB",
		Annotations: map[string]string{"owner": "team-sre"},
	})
	if err != nil {
		t.Fatalf("annotateEntity: %v", err)
	}
	want := tokenAuthor("full")
	if out.Annotation.Author == "" {
		t.Fatal("annotation author must not be empty")
	}
	if out.Annotation.Author != want {
		t.Fatalf("annotation author = %q, want %q", out.Annotation.Author, want)
	}
}

func TestAnnotateEntityRecordsLocalAuthor(t *testing.T) {
	s := newTestServer()
	store, err := annotations.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open annotations: %v", err)
	}
	defer func() { _ = store.Close() }()
	s.SetAnnotations(store)

	ctx := auth.WithPrincipal(auth.WithWritable(context.Background(), true), "local")

	_, out, err := s.annotateEntity(ctx, nil, AnnotateEntityInput{
		EntityID:    "01HOST_WEB",
		Annotations: map[string]string{"owner": "team-sre"},
	})
	if err != nil {
		t.Fatalf("annotateEntity: %v", err)
	}
	if out.Annotation.Author != "local" {
		t.Fatalf("annotation author = %q, want %q", out.Annotation.Author, "local")
	}
}

func TestAnnotateEntityAuditActor(t *testing.T) {
	s := newTestServer()
	store, err := annotations.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open annotations: %v", err)
	}
	defer func() { _ = store.Close() }()
	s.SetAnnotations(store)

	var buf bytes.Buffer
	s.audit = audit.New(&buf, nil)

	a := auth.NewWithRoles([]string{"full"}, nil, nil, nil)
	ctx := ctxWith(t, a, "full")

	if _, _, err := s.annotateEntity(ctx, nil, AnnotateEntityInput{
		EntityID:    "01HOST_WEB",
		Annotations: map[string]string{"owner": "team-sre"},
	}); err != nil {
		t.Fatalf("annotateEntity: %v", err)
	}

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("audit sink recorded no event")
	}
	want := `"actor":"` + tokenAuthor("full") + `"`
	if !strings.Contains(line, want) {
		t.Fatalf("audit line %q lacks %q", line, want)
	}
}

// keep httptest linked if middleware helpers change; request used only via ctxWith.
var _ = httptest.NewRequest
var _ http.HandlerFunc
