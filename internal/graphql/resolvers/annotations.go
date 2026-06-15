package resolvers

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/toise-dev/toise/internal/annotations"
	"github.com/toise-dev/toise/internal/auth"
	"github.com/toise-dev/toise/internal/graphql/generated"
	"github.com/toise-dev/toise/internal/model"
)

// Mutation returns the mutation resolver.
func (r *Resolver) Mutation() generated.MutationResolver { return &mutationResolver{r} }

// Entity returns the entity field resolver (for the annotations overlay).
func (r *Resolver) Entity() generated.EntityResolver { return &entityResolver{r} }

type mutationResolver struct{ *Resolver }
type entityResolver struct{ *Resolver }

// AnnotateEntity merges operator annotations onto an entity. Annotations are an
// overlay, NOT producer truth: they live in the per-tenant sidecar, never the
// event log. As the one write the read surface exposes, it requires a
// write-capable (full or tenant-scoped) token — a read-only token is refused.
func (r *mutationResolver) AnnotateEntity(ctx context.Context, id string, in []generated.AnnotationInput) (*generated.Annotation, error) {
	if r.Annotations == nil {
		return nil, fmt.Errorf("annotations are not enabled on this server")
	}
	if !auth.CanWrite(ctx) {
		return nil, fmt.Errorf("a read-only token cannot annotate; use a full-role token")
	}
	if len(in) == 0 {
		return nil, fmt.Errorf("at least one annotation is required (an empty value removes a key)")
	}
	if _, ok, _ := r.Graph.GetEntity(model.EntityID(id)); !ok {
		return nil, fmt.Errorf("no entity with id %q; annotate a known entity", id)
	}
	values := make(map[string]string, len(in))
	for _, e := range in {
		values[e.Key] = e.Value
	}
	a, err := r.Annotations.Set(id, values, "", r.now())
	if err != nil {
		return nil, err
	}
	return annotationToGQL(a), nil
}

// Annotations resolves an entity's operator annotations, or null when it has
// none or the sidecar is not enabled.
func (r *entityResolver) Annotations(_ context.Context, obj *generated.Entity) (*generated.Annotation, error) {
	if r.Resolver.Annotations == nil {
		return nil, nil
	}
	a, ok, err := r.Resolver.Annotations.Get(obj.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return annotationToGQL(a), nil
}

func annotationToGQL(a annotations.Annotation) *generated.Annotation {
	keys := make([]string, 0, len(a.Values))
	for k := range a.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	entries := make([]generated.AnnotationEntry, len(keys))
	for i, k := range keys {
		entries[i] = generated.AnnotationEntry{Key: k, Value: a.Values[k]}
	}
	out := &generated.Annotation{Values: entries}
	if a.Author != "" {
		author := a.Author
		out.Author = &author
	}
	if !a.UpdatedAt.IsZero() {
		s := a.UpdatedAt.UTC().Format(time.RFC3339Nano)
		out.UpdatedAt = &s
	}
	return out
}
