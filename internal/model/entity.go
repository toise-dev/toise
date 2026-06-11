package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	toisev1 "github.com/toise-dev/toise/proto/toise/v1"
)

// Entity is an infrastructure entity aligned with the OpenTelemetry entity data
// model. See ADR 0004 and ADR 0017.
type Entity struct {
	// ID is the stable logical entity ID (a ULID). It is assigned on first
	// sight and stable across identity changes.
	ID EntityID
	// Type is the entity type, e.g. TypeHost.
	Type string
	// Identity holds the identifying attributes. Their values together identify
	// the entity.
	Identity []KeyValue
	// Attributes holds descriptive (non-identifying) attributes.
	Attributes []KeyValue
	// SchemaURL versions the entity definition.
	SchemaURL string
}

// field/record separators for canonical identity encoding.
const (
	sepField  = "\x1e"
	sepRecord = "\x1f"
)

// IdentityHash returns a deterministic fingerprint of the entity's current
// identifying attributes: SHA-256 truncated to 128 bits, hex-encoded, prefixed
// by the entity type (e.g. "host:1a2b..."). The identifying set is canonicalized
// (keys sorted, values type-tagged) so the result is stable regardless of input
// order and unambiguous across value types. See ADR 0017.
func (e Entity) IdentityHash() string {
	kvs := make([]KeyValue, len(e.Identity))
	copy(kvs, e.Identity)
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].Key < kvs[j].Key })

	var b strings.Builder
	b.WriteString(e.Type)
	b.WriteString(sepRecord)
	for _, kv := range kvs {
		b.WriteString(kv.Key)
		b.WriteString(sepField)
		b.WriteString(kv.Value.canonical())
		b.WriteString(sepRecord)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return e.Type + ":" + hex.EncodeToString(sum[:idHashBytes])
}

// Validate checks the entity's structural invariants, including vocabulary
// membership (the strict default). It does not require a logical ID (that is
// assigned by the change engine). See ADR 0004.
func (e Entity) Validate() error { return e.validate(true) }

// ValidateShape checks everything Validate does EXCEPT vocabulary membership:
// an unknown entity.type with a sound identity passes. Deployments that opt
// into an open vocabulary (accept_unknown_types, #141) validate shape only —
// garbage detection stays (empty identity, malformed key-values), and identity
// hashing is type-prefixed, so unknown types are first-class identities with
// no fuzzy-merge risk (ADR 0018/0020 unchanged).
func (e Entity) ValidateShape() error { return e.validate(false) }

func (e Entity) validate(vocabulary bool) error {
	if e.Type == "" {
		return ErrEmptyType
	}
	if vocabulary && !IsKnownEntityType(e.Type) {
		return fmt.Errorf("%w: %q", ErrUnknownType, e.Type)
	}
	if len(e.Identity) == 0 {
		return ErrNoIdentity
	}
	if err := validateKeyValues(e.Identity); err != nil {
		return fmt.Errorf("identity: %w", err)
	}
	if err := validateKeyValues(e.Attributes); err != nil {
		return fmt.Errorf("attributes: %w", err)
	}
	return nil
}

// validateKeyValues checks for empty keys, duplicate keys, and unset values.
func validateKeyValues(kvs []KeyValue) error {
	seen := make(map[string]struct{}, len(kvs))
	for _, kv := range kvs {
		if kv.Key == "" {
			return ErrEmptyKey
		}
		if _, dup := seen[kv.Key]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateKey, kv.Key)
		}
		seen[kv.Key] = struct{}{}
		if !kv.Value.IsValid() {
			return fmt.Errorf("%w: key %q", ErrInvalidValue, kv.Key)
		}
	}
	return nil
}

// ToProto converts the entity to its protobuf representation. The identity hash
// is computed and embedded.
func (e Entity) ToProto() *toisev1.Entity {
	return &toisev1.Entity{
		EntityId:     string(e.ID),
		IdentityHash: e.IdentityHash(),
		Type:         e.Type,
		Identity:     kvsToProto(e.Identity),
		Attributes:   kvsToProto(e.Attributes),
		SchemaUrl:    e.SchemaURL,
	}
}

// EntityFromProto converts a protobuf entity to the domain type.
func EntityFromProto(p *toisev1.Entity) Entity {
	if p == nil {
		return Entity{}
	}
	return Entity{
		ID:         EntityID(p.GetEntityId()),
		Type:       p.GetType(),
		Identity:   kvsFromProto(p.GetIdentity()),
		Attributes: kvsFromProto(p.GetAttributes()),
		SchemaURL:  p.GetSchemaUrl(),
	}
}
