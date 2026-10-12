package resolvers

import (
	"context"

	"github.com/toise-dev/toise/internal/graphql/generated"
	"github.com/toise-dev/toise/internal/model"
)

// Relation resolves the fields of an edge that are not stored on it.
func (r *Resolver) Relation() generated.RelationResolver { return &relationResolver{r} }

type relationResolver struct{ *Resolver }

// AssertedBy names the producers currently asserting an edge, and CollectedBy
// the collection methods they used (#455).
//
// The engine has held both since per-producer reference counting (ADR 0019) and
// no read surface returned them, so two edges of one type — one an adjacency
// protocol states, one inferred from a forwarding table — were indistinguishable
// on the graph. A consumer drawing a map could not show a measurement
// differently from a correlation.
//
// Both return an empty list rather than an error when the engine is absent: a
// read surface wired without the engine is a configuration, not a failure, and
// an empty list reads as "not recorded" where an error would read as broken.
func (r *relationResolver) AssertedBy(_ context.Context, obj *generated.Relation) ([]string, error) {
	if r.Engine == nil || obj == nil {
		return []string{}, nil
	}
	out := r.Engine.RelationAssertingProducers(model.RelationID(obj.ID))
	if out == nil {
		return []string{}, nil
	}
	return out, nil
}

func (r *relationResolver) CollectedBy(_ context.Context, obj *generated.Relation) ([]string, error) {
	if r.Engine == nil || obj == nil {
		return []string{}, nil
	}
	out := r.Engine.RelationAssertingScopes(model.RelationID(obj.ID))
	if out == nil {
		return []string{}, nil
	}
	return out, nil
}
