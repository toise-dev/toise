package model

import (
	"fmt"

	toisev1 "github.com/toise-dev/toise/proto/toise/v1"
)

// Relation is a typed, directed edge between two entities, referenced by their
// logical IDs. See ADR 0004.
type Relation struct {
	// ID is derived from Type, From and To via ComputeRelationID.
	ID RelationID
	// Type is the relation type, e.g. RelRunsOn.
	Type string
	// From and To are the logical IDs of the connected entities.
	From EntityID
	To   EntityID
	// Attributes holds optional edge metadata.
	Attributes []KeyValue
	// Structural marks whether the relation's appearance/disappearance is
	// significant (suitable for alerting). See ADR 0006.
	Structural bool
}

// NewRelation builds a relation, deriving its ID and defaulting Structural from
// the type registry when the type is known.
func NewRelation(relType string, from, to EntityID, attrs ...KeyValue) Relation {
	r := Relation{
		ID:         ComputeRelationID(relType, from, to),
		Type:       relType,
		From:       from,
		To:         to,
		Attributes: attrs,
	}
	if def, ok := RelationDef(relType); ok {
		r.Structural = def.Structural
	}
	return r
}

// Validate checks the relation's structural invariants. Endpoint entity-type
// constraints (e.g. runs_on connects a process to a host) are enforced by the
// change engine, which has access to the entities; here we validate the type is
// known and the endpoints are present.
func (r Relation) Validate() error {
	if r.Type == "" {
		return ErrEmptyType
	}
	if _, ok := RelationDef(r.Type); !ok {
		return fmt.Errorf("%w: %q", ErrUnknownType, r.Type)
	}
	if r.From == "" || r.To == "" {
		return ErrEmptyEndpoint
	}
	if err := validateKeyValues(r.Attributes); err != nil {
		return fmt.Errorf("attributes: %w", err)
	}
	return nil
}

// ToProto converts the relation to its protobuf representation.
func (r Relation) ToProto() *toisev1.Relation {
	return &toisev1.Relation{
		RelationId:   string(r.ID),
		Type:         r.Type,
		FromEntityId: string(r.From),
		ToEntityId:   string(r.To),
		Attributes:   kvsToProto(r.Attributes),
		Structural:   r.Structural,
	}
}

// RelationFromProto converts a protobuf relation to the domain type.
func RelationFromProto(p *toisev1.Relation) Relation {
	if p == nil {
		return Relation{}
	}
	return Relation{
		ID:         RelationID(p.GetRelationId()),
		Type:       p.GetType(),
		From:       EntityID(p.GetFromEntityId()),
		To:         EntityID(p.GetToEntityId()),
		Attributes: kvsFromProto(p.GetAttributes()),
		Structural: p.GetStructural(),
	}
}
