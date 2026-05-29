package model

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/oklog/ulid/v2"
)

// EntityID is the stable logical identifier of an entity (a ULID string). It is
// assigned on first sight and survives identity changes. See ADR 0017.
type EntityID string

// RelationID is the deterministic identifier of a relation, derived from its
// type and endpoints.
type RelationID string

// idHashBytes is the number of SHA-256 bytes kept in hash-derived IDs and the
// identity hash: 16 bytes (128 bits) → negligible collision risk at 10^6 scale.
const idHashBytes = 16

// NewEntityID returns a fresh logical entity ID (a ULID). ULIDs are
// time-sortable and safe to generate concurrently.
func NewEntityID() EntityID {
	return EntityID(ulid.Make().String())
}

// NewEventID returns a fresh event ID (a ULID).
func NewEventID() string {
	return ulid.Make().String()
}

// ComputeRelationID derives a relation's ID from its type and endpoint logical
// IDs. Attributes are not part of a relation's identity.
func ComputeRelationID(relType string, from, to EntityID) RelationID {
	sum := sha256.Sum256([]byte(relType + "\x1f" + string(from) + "\x1f" + string(to)))
	return RelationID(relType + ":" + hex.EncodeToString(sum[:idHashBytes]))
}
