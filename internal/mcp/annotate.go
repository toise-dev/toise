package mcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toise-dev/toise/internal/annotations"
	"github.com/toise-dev/toise/internal/auth"
	"github.com/toise-dev/toise/internal/model"
)

// AnnotateEntityInput sets operator annotations on an entity.
type AnnotateEntityInput struct {
	EntityID    string            `json:"entity_id" jsonschema:"the logical entity id to annotate"`
	Annotations map[string]string `json:"annotations" jsonschema:"key/value notes to merge onto the entity; an empty value removes that key"`
}

// AnnotationOut is an entity's operator annotation rendered for an LLM.
type AnnotationOut struct {
	Values    map[string]string `json:"values"`
	Author    string            `json:"author,omitempty"`
	UpdatedAt string            `json:"updated_at,omitempty"`
}

// AnnotateEntityOutput carries the merged annotation.
type AnnotateEntityOutput struct {
	Annotation AnnotationOut `json:"annotation"`
}

func annotationOut(a annotations.Annotation) AnnotationOut {
	return AnnotationOut{Values: a.Values, Author: a.Author, UpdatedAt: formatTime(a.UpdatedAt)}
}

// annotateEntity attaches operator annotations to an entity. These are an
// overlay, NOT producer truth: they live in the per-tenant sidecar, never the
// event log. It is the one write the read surface exposes, so it requires a
// write-capable (full or tenant-scoped) token — a read-only token is refused.
func (s *Server) annotateEntity(ctx context.Context, _ *mcpsdk.CallToolRequest, in AnnotateEntityInput) (*mcpsdk.CallToolResult, AnnotateEntityOutput, error) {
	if s.ann == nil {
		return nil, AnnotateEntityOutput{}, fmt.Errorf("annotations are not enabled on this server")
	}
	if !auth.CanWrite(ctx) {
		return nil, AnnotateEntityOutput{}, fmt.Errorf("a read-only token cannot annotate; use a full-role token")
	}
	if in.EntityID == "" {
		return nil, AnnotateEntityOutput{}, fmt.Errorf("an entity_id is required")
	}
	if len(in.Annotations) == 0 {
		return nil, AnnotateEntityOutput{}, fmt.Errorf("at least one annotation key is required (an empty value removes a key)")
	}
	if _, ok, _ := s.graph.GetEntity(model.EntityID(in.EntityID)); !ok {
		return nil, AnnotateEntityOutput{}, fmt.Errorf("no entity with id %q; annotate a known entity (use find_entities)", in.EntityID)
	}
	a, err := s.ann.Set(in.EntityID, in.Annotations, "", s.now())
	if err != nil {
		return nil, AnnotateEntityOutput{}, err
	}
	return nil, AnnotateEntityOutput{Annotation: annotationOut(a)}, nil
}

// annotationFor returns an entity's annotation for read surfacing, or nil when
// it has none or annotations are not enabled.
func (s *Server) annotationFor(id string) *AnnotationOut {
	if s.ann == nil {
		return nil
	}
	a, ok, err := s.ann.Get(id)
	if err != nil || !ok {
		return nil
	}
	out := annotationOut(a)
	return &out
}
