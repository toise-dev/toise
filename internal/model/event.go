package model

import (
	"fmt"
	"time"

	toisev1 "github.com/toise-dev/toise/proto/toise/v1"
)

// SchemaVersion is the current Toise event schema version. It is independent of
// the OpenTelemetry spec version. See ADR 0015.
const SchemaVersion = "1.0"

// EntityEvent is a classified change about an entity. It is bi-temporal: see
// ADR 0005.
type EntityEvent struct {
	// EventID is a ULID identifying this event.
	EventID string
	// ChangeType classifies the change (must be an entity.* type).
	ChangeType ChangeType
	// Entity is the entity state carried by the event.
	Entity Entity
	// EventTime is when the fact became true in the real world (producer).
	EventTime time.Time
	// RecordedAt is when Toise recorded the event (ingestion).
	RecordedAt time.Time
	// SchemaVersion is the schema version of this event.
	SchemaVersion string
	// ChangedKeys lists the attribute keys that changed, for
	// attribute_updated/state_changed events.
	ChangedKeys []string
}

// RelationEvent is a classified change about a relation. Bi-temporal: ADR 0005.
type RelationEvent struct {
	EventID       string
	ChangeType    ChangeType
	Relation      Relation
	EventTime     time.Time
	RecordedAt    time.Time
	SchemaVersion string
	ChangedKeys   []string
}

// Event is the envelope stored in the append-only log. Exactly one of Entity or
// Relation is non-nil.
type Event struct {
	Entity   *EntityEvent
	Relation *RelationEvent
}

// Validate checks the entity event's invariants.
func (e EntityEvent) Validate() error {
	if e.ChangeType == ChangeUnspecified {
		return ErrChangeTypeUnset
	}
	if !e.ChangeType.IsEntity() {
		return fmt.Errorf("%w: %s is not an entity change", ErrChangeTypeMismatch, e.ChangeType)
	}
	if err := commonEventChecks(e.EventTime, e.RecordedAt, e.SchemaVersion); err != nil {
		return err
	}
	if err := e.Entity.Validate(); err != nil {
		return fmt.Errorf("entity: %w", err)
	}
	return nil
}

// Validate checks the relation event's invariants.
func (r RelationEvent) Validate() error {
	if r.ChangeType == ChangeUnspecified {
		return ErrChangeTypeUnset
	}
	if !r.ChangeType.IsRelation() {
		return fmt.Errorf("%w: %s is not a relation change", ErrChangeTypeMismatch, r.ChangeType)
	}
	if err := commonEventChecks(r.EventTime, r.RecordedAt, r.SchemaVersion); err != nil {
		return err
	}
	if err := r.Relation.Validate(); err != nil {
		return fmt.Errorf("relation: %w", err)
	}
	return nil
}

func commonEventChecks(eventTime, recordedAt time.Time, schemaVersion string) error {
	if eventTime.IsZero() {
		return ErrZeroEventTime
	}
	if recordedAt.IsZero() {
		return ErrZeroRecordedAt
	}
	if schemaVersion == "" {
		return ErrEmptySchemaVersion
	}
	return nil
}

func nanoOf(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func timeOf(nano int64) time.Time {
	if nano == 0 {
		return time.Time{}
	}
	return time.Unix(0, nano).UTC()
}

// ToProto converts the entity event to its protobuf representation.
func (e EntityEvent) ToProto() *toisev1.EntityEvent {
	return &toisev1.EntityEvent{
		EventId:            e.EventID,
		ChangeType:         e.ChangeType.toProto(),
		Entity:             e.Entity.ToProto(),
		EventTimeUnixNano:  nanoOf(e.EventTime),
		RecordedAtUnixNano: nanoOf(e.RecordedAt),
		SchemaVersion:      e.SchemaVersion,
		ChangedKeys:        e.ChangedKeys,
	}
}

// EntityEventFromProto converts a protobuf entity event to the domain type.
func EntityEventFromProto(p *toisev1.EntityEvent) EntityEvent {
	if p == nil {
		return EntityEvent{}
	}
	return EntityEvent{
		EventID:       p.GetEventId(),
		ChangeType:    changeTypeFromProto(p.GetChangeType()),
		Entity:        EntityFromProto(p.GetEntity()),
		EventTime:     timeOf(p.GetEventTimeUnixNano()),
		RecordedAt:    timeOf(p.GetRecordedAtUnixNano()),
		SchemaVersion: p.GetSchemaVersion(),
		ChangedKeys:   p.GetChangedKeys(),
	}
}

// ToProto converts the relation event to its protobuf representation.
func (r RelationEvent) ToProto() *toisev1.RelationEvent {
	return &toisev1.RelationEvent{
		EventId:            r.EventID,
		ChangeType:         r.ChangeType.toProto(),
		Relation:           r.Relation.ToProto(),
		EventTimeUnixNano:  nanoOf(r.EventTime),
		RecordedAtUnixNano: nanoOf(r.RecordedAt),
		SchemaVersion:      r.SchemaVersion,
		ChangedKeys:        r.ChangedKeys,
	}
}

// RelationEventFromProto converts a protobuf relation event to the domain type.
func RelationEventFromProto(p *toisev1.RelationEvent) RelationEvent {
	if p == nil {
		return RelationEvent{}
	}
	return RelationEvent{
		EventID:       p.GetEventId(),
		ChangeType:    changeTypeFromProto(p.GetChangeType()),
		Relation:      RelationFromProto(p.GetRelation()),
		EventTime:     timeOf(p.GetEventTimeUnixNano()),
		RecordedAt:    timeOf(p.GetRecordedAtUnixNano()),
		SchemaVersion: p.GetSchemaVersion(),
		ChangedKeys:   p.GetChangedKeys(),
	}
}

// ToProto converts the event envelope to its protobuf representation.
func (e Event) ToProto() *toisev1.Event {
	switch {
	case e.Entity != nil:
		return &toisev1.Event{Event: &toisev1.Event_EntityEvent{EntityEvent: e.Entity.ToProto()}}
	case e.Relation != nil:
		return &toisev1.Event{Event: &toisev1.Event_RelationEvent{RelationEvent: e.Relation.ToProto()}}
	default:
		return &toisev1.Event{}
	}
}

// EventFromProto converts a protobuf event envelope to the domain type.
func EventFromProto(p *toisev1.Event) Event {
	if p == nil {
		return Event{}
	}
	switch x := p.GetEvent().(type) {
	case *toisev1.Event_EntityEvent:
		ev := EntityEventFromProto(x.EntityEvent)
		return Event{Entity: &ev}
	case *toisev1.Event_RelationEvent:
		ev := RelationEventFromProto(x.RelationEvent)
		return Event{Relation: &ev}
	default:
		return Event{}
	}
}

// Validate checks the envelope contains exactly one valid event.
func (e Event) Validate() error {
	switch {
	case e.Entity != nil && e.Relation != nil:
		return fmt.Errorf("%w: envelope has both entity and relation events", ErrChangeTypeMismatch)
	case e.Entity != nil:
		return e.Entity.Validate()
	case e.Relation != nil:
		return e.Relation.Validate()
	default:
		return ErrMissingEntity
	}
}
