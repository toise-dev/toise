package model

import toisev1 "github.com/toise-dev/toise/proto/toise/v1"

// ChangeType classifies an event per the Toise change taxonomy. See ADR 0006.
type ChangeType int

// Change types. The zero value is ChangeUnspecified. Ordering matches the proto
// enum.
const (
	ChangeUnspecified ChangeType = iota
	EntityCreated
	EntityDeleted
	// EntityIdentityChanged is retained for wire compatibility and for replaying
	// historical logs, but the change engine no longer emits it under exact
	// identity matching (ADR 0018).
	EntityIdentityChanged
	EntityAttributeUpdated
	EntityStateChanged
	EntityUnchanged
	RelationAdded
	RelationRemoved
	RelationAttributeChanged
)

var changeTypeNames = map[ChangeType]string{
	ChangeUnspecified:        "unspecified",
	EntityCreated:            "entity.created",
	EntityDeleted:            "entity.deleted",
	EntityIdentityChanged:    "entity.identity_changed",
	EntityAttributeUpdated:   "entity.attribute_updated",
	EntityStateChanged:       "entity.state_changed",
	EntityUnchanged:          "entity.unchanged",
	RelationAdded:            "relation.added",
	RelationRemoved:          "relation.removed",
	RelationAttributeChanged: "relation.attribute_changed",
}

// String returns the dotted taxonomy name, e.g. "entity.created".
func (c ChangeType) String() string {
	if n, ok := changeTypeNames[c]; ok {
		return n
	}
	return "unspecified"
}

// IsEntity reports whether the change type concerns an entity.
func (c ChangeType) IsEntity() bool {
	return c >= EntityCreated && c <= EntityUnchanged
}

// IsRelation reports whether the change type concerns a relation.
func (c ChangeType) IsRelation() bool {
	return c >= RelationAdded && c <= RelationAttributeChanged
}

func (c ChangeType) toProto() toisev1.ChangeType {
	return toisev1.ChangeType(c)
}

func changeTypeFromProto(p toisev1.ChangeType) ChangeType {
	return ChangeType(p)
}
