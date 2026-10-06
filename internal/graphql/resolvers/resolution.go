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
// event times asks for it, one listing an inventory does not.
//
// When no producer declares a cadence it reports "none" with the consequence
// rather than returning null (#363). Such an entity never expires, and a reader
// cannot tell it apart from one asserted a minute ago unless the answer admits
// it. Refusing to invent a bound is the honest part and is unchanged; staying
// silent about the absence was the defect.
// noLivenessPromise is kept byte-identical in wording to the MCP surface's
// constant: two phrasings of one fact in two surfaces is where the next
// divergence starts.
const noLivenessPromise = "no producer declared a refresh interval for this entity, so Toise has no liveness promise to hold it to: its presence here means it was observed at least once and never explicitly deleted, and it will NOT expire on its own however long its producers stay silent. Read it as \"something asserted this and nothing has contradicted it\", not as \"a producer vouched for this recently\". The timestamps carry no resolution bound either, so no ordering or causal conclusion may be drawn from how close two of them are. Toise does not invent a default interval: that would be a promise no producer made."

func (r *entityResolver) Resolution(_ context.Context, obj *generated.Entity) (*generated.Resolution, error) {
	if r.Engine == nil {
		return nil, nil
	}
	interval, ok := r.Engine.ObservationInterval(model.EntityID(obj.ID))
	if !ok {
		return &generated.Resolution{ObservationInterval: "none", Meaning: noLivenessPromise}, nil
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
