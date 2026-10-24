package model

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

func TestValueKindsAndCanonical(t *testing.T) {
	cases := []struct {
		v    Value
		kind ValueKind
		can  string
	}{
		{StringValue("1"), KindString, "s:1"},
		{IntValue(1), KindInt, "i:1"},
		{DoubleValue(1.5), KindDouble, "d:1.5"},
		{BoolValue(true), KindBool, "b:true"},
		{Value{}, KindInvalid, "n:"},
	}
	for _, c := range cases {
		if c.v.Kind() != c.kind {
			t.Errorf("Kind()=%v want %v", c.v.Kind(), c.kind)
		}
		if got := c.v.canonical(); got != c.can {
			t.Errorf("canonical()=%q want %q", got, c.can)
		}
	}
	// The string "1" and the integer 1 must not share a canonical form.
	if StringValue("1").canonical() == IntValue(1).canonical() {
		t.Error("string and int canonical forms collide")
	}
	if (Value{}).IsValid() {
		t.Error("zero Value should be invalid")
	}
	if !StringValue("x").IsValid() {
		t.Error("string Value should be valid")
	}
}

func TestValueAccessorsAndProto(t *testing.T) {
	if StringValue("a").Str() != "a" || IntValue(7).Int() != 7 ||
		DoubleValue(2.5).Double() != 2.5 || !BoolValue(true).Bool() {
		t.Error("accessor mismatch")
	}
	for _, v := range []Value{StringValue("a"), IntValue(7), DoubleValue(2.5), BoolValue(true), {}} {
		if got := valueFromProto(v.toProto()); !reflect.DeepEqual(got, v) {
			t.Errorf("value proto round-trip: got %+v want %+v", got, v)
		}
	}
	if v := valueFromProto(nil); v.IsValid() {
		t.Error("valueFromProto(nil) should be invalid")
	}
}

func hostIdentity() []KeyValue {
	return []KeyValue{
		{Key: "host.name", Value: StringValue("web-1")},
		{Key: "host.id", Value: StringValue("abc")},
	}
}

func TestIdentityHashDeterministicAndOrderIndependent(t *testing.T) {
	e1 := Entity{Type: TypeHost, Identity: hostIdentity()}
	reordered := []KeyValue{hostIdentity()[1], hostIdentity()[0]}
	e2 := Entity{Type: TypeHost, Identity: reordered}
	if e1.IdentityHash() != e2.IdentityHash() {
		t.Error("identity hash must be order-independent")
	}
	if e1.IdentityHash() == "" {
		t.Error("identity hash empty")
	}
	// Prefix is the type.
	if got := e1.IdentityHash(); got[:len(TypeHost)+1] != TypeHost+":" {
		t.Errorf("hash %q not prefixed by type", got)
	}
}

func TestIdentityHashChangesWithIdentityOnly(t *testing.T) {
	base := Entity{Type: TypeHost, Identity: hostIdentity()}
	// Changing a descriptive attribute does NOT change the identity hash.
	withAttr := base
	withAttr.Attributes = []KeyValue{{Key: "os", Value: StringValue("linux")}}
	if base.IdentityHash() != withAttr.IdentityHash() {
		t.Error("descriptive attribute must not affect identity hash")
	}
	// Changing an identifying attribute DOES change the hash.
	changed := Entity{Type: TypeHost, Identity: []KeyValue{
		{Key: "host.name", Value: StringValue("web-2")},
		{Key: "host.id", Value: StringValue("abc")},
	}}
	if base.IdentityHash() == changed.IdentityHash() {
		t.Error("identifying attribute change must change the hash")
	}
}

func TestEntityValidate(t *testing.T) {
	good := Entity{Type: TypeHost, Identity: hostIdentity()}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid entity rejected: %v", err)
	}
	cases := []struct {
		name string
		e    Entity
		want error
	}{
		{"empty type", Entity{Identity: hostIdentity()}, ErrEmptyType},
		{"unknown type", Entity{Type: "nope", Identity: hostIdentity()}, ErrUnknownType},
		{"no identity", Entity{Type: TypeHost}, ErrNoIdentity},
		{"empty key", Entity{Type: TypeHost, Identity: []KeyValue{{Key: "", Value: StringValue("x")}}}, ErrEmptyKey},
		{"dup key", Entity{Type: TypeHost, Identity: []KeyValue{{Key: "k", Value: StringValue("a")}, {Key: "k", Value: StringValue("b")}}}, ErrDuplicateKey},
		{"invalid value", Entity{Type: TypeHost, Identity: []KeyValue{{Key: "k"}}}, ErrInvalidValue},
	}
	for _, c := range cases {
		if err := c.e.Validate(); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v want %v", c.name, err, c.want)
		}
	}
}

func TestEntityProtoRoundTrip(t *testing.T) {
	e := Entity{
		ID:         NewEntityID(),
		Type:       TypeHost,
		Identity:   hostIdentity(),
		Attributes: []KeyValue{{Key: "os", Value: StringValue("linux")}},
		SchemaURL:  "https://schemas.toise.dev/host/1.0",
	}
	got := EntityFromProto(e.ToProto())
	if !reflect.DeepEqual(got, e) {
		t.Errorf("entity round-trip mismatch:\n got %+v\nwant %+v", got, e)
	}
	if EntityFromProto(nil).Type != "" {
		t.Error("EntityFromProto(nil) should be zero")
	}
}

func TestRelationNewValidateAndProto(t *testing.T) {
	from, to := NewEntityID(), NewEntityID()
	r := NewRelation(RelRunsOn, from, to)
	if !r.Structural {
		t.Error("runs_on should default to structural from registry")
	}
	if r.ID != ComputeRelationID(RelRunsOn, from, to) {
		t.Error("relation ID not derived")
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("valid relation rejected: %v", err)
	}
	if got := RelationFromProto(r.ToProto()); !reflect.DeepEqual(got, r) {
		t.Errorf("relation round-trip mismatch:\n got %+v\nwant %+v", got, r)
	}

	if err := (Relation{Type: "nope", From: from, To: to}).Validate(); !errors.Is(err, ErrUnknownType) {
		t.Error("unknown relation type should fail")
	}
	if err := (Relation{Type: RelRunsOn, From: from}).Validate(); !errors.Is(err, ErrEmptyEndpoint) {
		t.Error("missing endpoint should fail")
	}
	if err := (Relation{}).Validate(); !errors.Is(err, ErrEmptyType) {
		t.Error("empty relation type should fail")
	}
}

func TestComputeRelationIDDistinct(t *testing.T) {
	a, b := NewEntityID(), NewEntityID()
	if ComputeRelationID(RelRunsOn, a, b) == ComputeRelationID(RelRunsOn, b, a) {
		t.Error("relation ID should be direction-sensitive")
	}
}

func TestChangeType(t *testing.T) {
	if EntityCreated.String() != "entity.created" || RelationAdded.String() != "relation.added" {
		t.Error("change type names wrong")
	}
	if ChangeType(99).String() != "unspecified" {
		t.Error("unknown change type should render unspecified")
	}
	if !EntityCreated.IsEntity() || EntityCreated.IsRelation() {
		t.Error("EntityCreated classification wrong")
	}
	if !RelationRemoved.IsRelation() || RelationRemoved.IsEntity() {
		t.Error("RelationRemoved classification wrong")
	}
	for c := ChangeUnspecified; c <= RelationAttributeChanged; c++ {
		if changeTypeFromProto(c.toProto()) != c {
			t.Errorf("change type proto round-trip failed for %v", c)
		}
	}
}

func TestRegistry(t *testing.T) {
	if !IsKnownEntityType(TypeHost) || IsKnownEntityType("nope") {
		t.Error("entity type registry wrong")
	}
	if def, ok := RelationDef(RelListensOn); !ok || def.From != TypeServiceListener || def.To != TypeNetworkInterface {
		t.Error("relation def wrong")
	}
	if len(EntityTypes()) != 18 || len(RelationTypes()) != 18 {
		t.Errorf("registry counts: %d entity, %d relation", len(EntityTypes()), len(RelationTypes()))
	}
	// compute.vm (hypervisor-discovered VM), container and pod are registered
	// compute resources, accepted under the strict vocabulary without
	// --accept-unknown-types. A pod that reached the boundary and was refused
	// would be a producer emitting against a Toise too old to know the type.
	if !IsKnownEntityType(TypeComputeVM) || !IsKnownEntityType(TypeContainer) || !IsKnownEntityType(TypePod) {
		t.Error("compute.vm, container and pod must be known entity types")
	}
	if !IsKnownEntityType(TypeNetworkSegment) {
		t.Error("network.segment must be a known entity type")
	}
	// network.neighbor is accepted under the strict vocabulary too: a producer
	// emitting one against a Toise too old to know the type gets a per-record
	// rejection, which is the whole reason the type has to ship before the
	// producer enables it.
	if !IsKnownEntityType(TypeNetworkNeighbor) {
		t.Error("network.neighbor must be a known entity type")
	}
	// An observation is not a failure path, in either direction. Measured before
	// this was pinned: with impact flowing from the interface, impact_of on a host
	// walked has_interface then every neighbor_of and reported each ARP neighbor as
	// impacted — a host with a few hundred ARP entries would claim a few hundred
	// third-party devices, which is not what a blast radius means. Losing sight of
	// a segment is a coverage question and does not belong in that answer.
	if def, ok := RelationDef(RelNeighborOf); !ok ||
		def.From != TypeNetworkNeighbor || def.To != TypeNetworkInterface || def.Impact != ImpactNone {
		t.Errorf("neighbor_of must run network.neighbor -> network.interface and carry no impact, got %+v", def)
	}
	// A segment is declared by its cluster and joined by a workload, and the two
	// edges flow opposite ways: the cluster failing drops the segments it
	// declares (From->To, like has_interface and has_route), while the segment
	// failing takes what is attached to it (To->From).
	if def, ok := RelationDef(RelHasSegment); !ok || def.From != TypeServiceInstance || def.To != TypeNetworkSegment || def.Impact != ImpactFromTo {
		t.Error("has_segment relation def wrong")
	}
	if def, ok := RelationDef(RelAttachedTo); !ok || def.To != TypeNetworkSegment || def.Impact != ImpactToFrom {
		t.Error("attached_to relation def wrong")
	}
	// depends_on (connection topology, #184) targets an observable network.endpoint.
	if def, ok := RelationDef(RelDependsOn); !ok || def.From != TypeServiceInstance || def.To != TypeNetworkEndpoint {
		t.Error("depends_on relation def wrong")
	}
	// connected_to (topology-as-entities, ADR 0022) is registered as a bare
	// interface<->interface edge.
	if def, ok := RelationDef(RelConnectedTo); !ok || def.From != TypeNetworkInterface || def.To != TypeNetworkInterface {
		t.Error("connected_to relation def wrong")
	}
	// has_route attaches a routing-table entry to its device (network.route model).
	if def, ok := RelationDef(RelHasRoute); !ok || def.From != TypeNetworkDevice || def.To != TypeNetworkRoute {
		t.Error("has_route relation def wrong")
	}
	// producer-vocabulary types are registered
	for _, typ := range []string{TypeServiceInstance, TypeDatabase, TypeNetworkDevice} {
		if !IsKnownEntityType(typ) {
			t.Errorf("producer entity type %q not registered", typ)
		}
	}
	if def, ok := RelationDef(RelMonitors); !ok || def.From != TypeServiceInstance || !def.Structural {
		t.Error("monitors relation def wrong")
	}
	for _, rel := range []string{RelRoutesVia, RelForwardsTo, RelAdjacentTo} {
		if def, ok := RelationDef(rel); !ok || def.From != TypeNetworkDevice || def.To != TypeNetworkDevice {
			t.Errorf("network relation %q def wrong", rel)
		}
	}
}

func TestNewEntityIDUnique(t *testing.T) {
	a, b := NewEntityID(), NewEntityID()
	if a == b {
		t.Error("entity IDs should be unique")
	}
	if _, err := ulid.Parse(string(a)); err != nil {
		t.Errorf("entity ID is not a valid ULID: %v", err)
	}
	if _, err := ulid.Parse(NewEventID()); err != nil {
		t.Errorf("event ID is not a valid ULID: %v", err)
	}
}

func sampleEntityEvent() EntityEvent {
	return EntityEvent{
		EventID:       NewEventID(),
		ChangeType:    EntityCreated,
		Entity:        Entity{ID: NewEntityID(), Type: TypeHost, Identity: hostIdentity()},
		EventTime:     time.Unix(1700000000, 123).UTC(),
		RecordedAt:    time.Unix(1700000005, 0).UTC(),
		SchemaVersion: SchemaVersion,
	}
}

func TestEntityEventValidateAndProto(t *testing.T) {
	ev := sampleEntityEvent()
	if err := ev.Validate(); err != nil {
		t.Fatalf("valid entity event rejected: %v", err)
	}
	if got := EntityEventFromProto(ev.ToProto()); !reflect.DeepEqual(got, ev) {
		t.Errorf("entity event round-trip mismatch:\n got %+v\nwant %+v", got, ev)
	}

	bad := ev
	bad.ChangeType = ChangeUnspecified
	if !errors.Is(bad.Validate(), ErrChangeTypeUnset) {
		t.Error("unset change type should fail")
	}
	bad = ev
	bad.ChangeType = RelationAdded
	if !errors.Is(bad.Validate(), ErrChangeTypeMismatch) {
		t.Error("relation change on entity event should fail")
	}
	bad = ev
	bad.EventTime = time.Time{}
	if !errors.Is(bad.Validate(), ErrZeroEventTime) {
		t.Error("zero event time should fail")
	}
	bad = ev
	bad.RecordedAt = time.Time{}
	if !errors.Is(bad.Validate(), ErrZeroRecordedAt) {
		t.Error("zero recorded_at should fail")
	}
	bad = ev
	bad.SchemaVersion = ""
	if !errors.Is(bad.Validate(), ErrEmptySchemaVersion) {
		t.Error("empty schema version should fail")
	}
}

func TestRelationEventValidateAndProto(t *testing.T) {
	from, to := NewEntityID(), NewEntityID()
	ev := RelationEvent{
		EventID:       NewEventID(),
		ChangeType:    RelationAdded,
		Relation:      NewRelation(RelRunsOn, from, to),
		EventTime:     time.Unix(1700000000, 0).UTC(),
		RecordedAt:    time.Unix(1700000001, 0).UTC(),
		SchemaVersion: SchemaVersion,
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("valid relation event rejected: %v", err)
	}
	if got := RelationEventFromProto(ev.ToProto()); !reflect.DeepEqual(got, ev) {
		t.Errorf("relation event round-trip mismatch")
	}
	bad := ev
	bad.ChangeType = EntityCreated
	if !errors.Is(bad.Validate(), ErrChangeTypeMismatch) {
		t.Error("entity change on relation event should fail")
	}
}

func TestEventEnvelope(t *testing.T) {
	ee := sampleEntityEvent()
	env := Event{Entity: &ee}
	if err := env.Validate(); err != nil {
		t.Fatalf("entity envelope invalid: %v", err)
	}
	if got := EventFromProto(env.ToProto()); !reflect.DeepEqual(*got.Entity, ee) {
		t.Error("entity envelope round-trip mismatch")
	}

	from, to := NewEntityID(), NewEntityID()
	re := RelationEvent{EventID: NewEventID(), ChangeType: RelationAdded, Relation: NewRelation(RelRunsOn, from, to), EventTime: time.Unix(1, 0).UTC(), RecordedAt: time.Unix(2, 0).UTC(), SchemaVersion: SchemaVersion}
	renv := Event{Relation: &re}
	if got := EventFromProto(renv.ToProto()); !reflect.DeepEqual(*got.Relation, re) {
		t.Error("relation envelope round-trip mismatch")
	}

	if err := (Event{Entity: &ee, Relation: &re}).Validate(); err == nil {
		t.Error("envelope with both should be invalid")
	}
	if err := (Event{}).Validate(); !errors.Is(err, ErrMissingEntity) {
		t.Error("empty envelope should be invalid")
	}
}

// TestSameAsRegistration pins ADR 0020 Lot A: same_as is a registered,
// non-structural, no-impact identity-belief edge.
func TestSameAsRegistration(t *testing.T) {
	def, ok := RelationDef(RelSameAs)
	if !ok {
		t.Fatal("same_as must be a registered relation type (else the boundary rejects it)")
	}
	if def.Structural {
		t.Error("same_as must be non-structural: a belief assertion is not an alert-worthy structural change")
	}
	if def.Impact != ImpactNone {
		t.Errorf("same_as Impact = %v, want ImpactNone (an alias is not a failure path)", def.Impact)
	}
	if ImpactFlowOf(RelSameAs) != ImpactNone {
		t.Error("ImpactFlowOf(same_as) must be ImpactNone")
	}
}

// TestNewRelationsCarryNoImpact pins the two impact decisions of 0.21.0, and the
// registration check is what keeps it from passing for the wrong reason.
//
// ImpactFlowOf returns ImpactBoth for an UNREGISTERED type — over-reporting
// being the safer default when nothing is known. So a test asserting only
// "impact is None" would fail loudly if the registration were dropped, which
// sounds fine, but the reverse matters more: it would pass on nothing at all if
// the constant were renamed, because an unknown type never reaches the
// assertion. Checking registration first makes the test about the DECISION
// rather than about the default.
func TestNewRelationsCarryNoImpact(t *testing.T) {
	for _, tc := range []struct {
		relType string
		why     string
	}{
		{
			relType: RelResolvesTo,
			why: "the dependent side is a third party's record; measured, an impact there made impact_of " +
				"on a host report every ARP cache entry — a few hundred devices on one failure",
		},
		{
			relType: RelAggregatedInto,
			why: "a member failing does not take the aggregate down, that being the entire point of a LAG; " +
				"the real impact is conditional on being the LAST active member, and the impact walk does not count",
		},
	} {
		def, ok := RelationDef(tc.relType)
		if !ok {
			t.Errorf("%s is not registered, so ImpactFlowOf would answer the unknown-type default "+
				"and this test would say nothing about the decision", tc.relType)
			continue
		}
		if got := ImpactFlowOf(tc.relType); got != ImpactNone {
			t.Errorf("%s impact = %v, want ImpactNone. Not an oversight: %s", tc.relType, got, tc.why)
		}
		if def.Impact != ImpactNone {
			t.Errorf("%s is registered with impact %v; the table and ImpactFlowOf disagree", tc.relType, def.Impact)
		}
	}
	// And the structural flags, which differ on purpose: a port leaving a
	// port-channel is a degradation operators alert on, while SNMP discovering
	// that a neighbor resolves to a port is coverage improving and would flood
	// the change feed.
	for relType, wantStructural := range map[string]bool{
		RelResolvesTo:     false,
		RelAggregatedInto: true,
	} {
		def, ok := RelationDef(relType)
		if !ok {
			continue // already reported above
		}
		if def.Structural != wantStructural {
			t.Errorf("%s structural = %v, want %v", relType, def.Structural, wantStructural)
		}
	}
}
