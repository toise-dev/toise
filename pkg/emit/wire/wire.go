// Package wire is the single in-repo spelling of the Toise entity-events wire
// vocabulary (the merged OTel entity-events convention; see ADR 0009, ADR 0022,
// and docs/data-model/otel-mapping.md). The emit SDK, the conformance kit, and
// Toise's own ingest boundary all import these constants, so producer and
// consumer can no longer drift apart one literal at a time. The frozen fixture
// (testdata/fixture_v1.bin) still pins the contract against the outside world;
// this package only pins the repo's code against itself.
//
// It is deliberately stdlib-only: importing the vocabulary must never pull a
// protocol stack into a producer's module graph.
package wire

// LogRecord EventName values. Entity lifecycle events are identified by the
// OTel LogRecord EventName, not by an attribute.
const (
	// EventEntityState asserts an entity's current state (create or update).
	EventEntityState = "entity.state"
	// EventEntityDelete releases the emitting producer's reference to the
	// entity; the entity is deleted when the last producer releases it
	// (ADR 0019).
	EventEntityDelete = "entity.delete"
)

// LogRecord attribute keys on entity events.
const (
	// AttrEntityType is the entity type, e.g. "host" or "service.listener".
	AttrEntityType = "entity.type"
	// AttrEntityID is the identifying attribute map. Exact-match identity:
	// every key and value counts (ADR 0018).
	AttrEntityID = "entity.id"
	// AttrEntityDescription is the descriptive (non-identifying) attribute map.
	AttrEntityDescription = "entity.description"
	// AttrEntityReportInterval is the producer's heartbeat cadence in SECONDS
	// (int). It arms the consumer's liveness backstop: an entity not
	// re-asserted within its interval is expired. It is a backstop, not a
	// primary delete signal.
	AttrEntityReportInterval = "entity.report.interval"
	// AttrEntityDeleteReason is the optional motive a producer attaches to an
	// entity.delete event (e.g. "terminated", "expired", "evicted",
	// "user_requested", "scaled_down"). It is an OPEN enum: those values are
	// illustrative, not exhaustive, and the consumer must never reject an
	// unrecognized one. Only meaningful on entity.delete events.
	AttrEntityDeleteReason = "entity.delete.reason"
	// AttrEntityRelationships is the embedded-relationship array an
	// entity.state event MAY carry: each element is a descriptor map (see the
	// Rel* keys). This is the sole on-wire relationship form (ADR 0022): the
	// source is the entity carrying the array, and removal is by absence on
	// re-emit.
	AttrEntityRelationships = "entity.relationships"
)

// Keys inside one entity.relationships descriptor map.
const (
	// RelType is the relationship type, e.g. "runs_on".
	RelType = "relationship.type"
	// RelTargetType is the target entity's type — spelled exactly like the
	// record-level key on purpose: a descriptor is a miniature entity
	// reference.
	RelTargetType = AttrEntityType
	// RelTargetID is the target entity's identity map.
	RelTargetID = AttrEntityID
)

// OTLP Resource attribute keys on a ResourceLogs carrying entity events.
const (
	// ResServiceName names the producing service (informational).
	ResServiceName = "service.name"
	// ResServiceInstanceID identifies the producing agent instance. It is the
	// producer identity liveness is reference-counted on (ADR 0019): set it
	// stable per producer instance, or every producer collapses into one
	// anonymous reference.
	ResServiceInstanceID = "service.instance.id"
)
