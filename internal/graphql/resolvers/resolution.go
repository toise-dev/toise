package resolvers

import (
	"context"

	"github.com/toise-dev/toise/internal/graphql/generated"
	"github.com/toise-dev/toise/internal/model"
)

// Resolution answers how finely a timestamp about this entity may be read.
//
// It is a field resolver rather than a fixed part of every Entity payload
// because the cost is a lookup and the need is occasional: a client comparing
// event times asks for it, one listing an inventory does not. Null when no live
// producer declares a cadence — saying nothing is the honest answer, where
// inventing a bound would be worse than the silence.
func (r *entityResolver) Resolution(_ context.Context, obj *generated.Entity) (*generated.Resolution, error) {
	if r.Engine == nil {
		return nil, nil
	}
	interval, ok := r.Engine.ObservationInterval(model.EntityID(obj.ID))
	if !ok {
		return nil, nil
	}
	d := interval.String()
	return &generated.Resolution{
		ObservationInterval: d,
		Meaning: "every eventTime about this entity is when a producer OBSERVED the fact, not when it became true. This is the liveness interval its producers DECLARED, padded above their real reporting cadence — an upper bound on the uncertainty, never an under-statement: the change happened somewhere within " +
			d + " before its eventTime, often much closer. Two changes less than " + d +
			" apart therefore carry no guaranteed ordering, and a causal conclusion drawn from a gap that small — including against an external clock — needs evidence from outside Toise. " +
			"This is the coarsest declared interval among the producers referencing this entity right now; it is not recoverable for past observations.",
	}, nil
}
