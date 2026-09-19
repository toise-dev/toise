package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
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

// MatchAll reports whether every wanted key/value is present as a string-equal
// attribute in the entity's identity or descriptive attributes (AND semantics).
// Values are compared via Value.Display (untyped), so the filter is the same
// across the MCP find_entities tool and the GraphQL entities query.
func (e Entity) MatchAll(want map[string]string) bool {
	for k, v := range want {
		if !hasAttr(e.Identity, k, v) && !hasAttr(e.Attributes, k, v) {
			return false
		}
	}
	return true
}

func hasAttr(kvs []KeyValue, key, val string) bool {
	for _, kv := range kvs {
		if kv.Key == key && kv.Value.Display() == val {
			return true
		}
	}
	return false
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
	// Hot in both directions: every ingested observation hashes to find its
	// entity, and every rendered entity carries its fingerprint. The encoding is
	// byte-for-byte what it has always been — stored hashes depend on it — but
	// it is built on the stack for the shapes that actually occur (few keys,
	// short values) instead of through a chain of temporary strings.
	var idents [4]KeyValue
	kvs := idents[:0]
	if len(e.Identity) > cap(kvs) {
		kvs = make([]KeyValue, 0, len(e.Identity))
	}
	kvs = append(kvs, e.Identity...)
	if len(kvs) > 1 {
		slices.SortFunc(kvs, func(a, b KeyValue) int { return strings.Compare(a.Key, b.Key) })
	}

	var scratch [256]byte
	buf := scratch[:0]
	buf = append(buf, e.Type...)
	buf = append(buf, sepRecord...)
	for _, kv := range kvs {
		buf = append(buf, kv.Key...)
		buf = append(buf, sepField...)
		buf = kv.Value.appendCanonical(buf)
		buf = append(buf, sepRecord...)
	}
	sum := sha256.Sum256(buf)

	var hexed [idHashBytes * 2]byte
	hex.Encode(hexed[:], sum[:idHashBytes])
	var sb strings.Builder
	sb.Grow(len(e.Type) + 1 + len(hexed))
	sb.WriteString(e.Type)
	sb.WriteByte(':')
	sb.Write(hexed[:])
	return sb.String()
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
