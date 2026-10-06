package model

// GovernanceAttribute is a cross-cutting, operator-supplied descriptive
// attribute that MAY appear on any entity type — ownership, criticality,
// physical location, lifecycle. The vocabulary exists so consumers can discover
// the canonical keys (and filter on them) and producers emit them consistently.
//
// It is ADVISORY only. Per ADR 0022 the engine stores facts as-is: Toise never
// requires, rejects, normalizes, or derives these — they are producer-asserted
// truth carried as plain descriptive attributes. The registry feeds the
// describe surfaces and nothing else; an entity carrying none of them is valid.
//
// service.namespace and service.criticality are reused verbatim from OTel
// semconv (service-scoped there; Toise applies the same keys to any entity).
//
// The keys Toise invented live under senhub.*, not under entity.*. The semconv
// naming rule is explicit: "It is not recommended to use existing OpenTelemetry
// semantic convention namespace as a prefix for a new company- or
// application-specific attribute name." entity.* is the entity-events
// convention's own namespace, so inventing governance keys inside it was a
// squat: if upstream ever defines entity.owner.team differently, data already in
// the field means two things at once and nothing can tell them apart.
//
// Previous spellings stay listed on each entry rather than being deleted.
// Governance attributes are descriptive, so the engine kept whatever producers
// sent and data emitted before the rename is still in the graph: a consumer
// needs to know both the key to use and the key to find.
type GovernanceAttribute struct {
	Key     string   // the attribute key producers should use
	Summary string   // one-line meaning, surfaced to consumers
	Example string   // an example value, to show the shape
	Values  []string // well-known values when the key is an open enum (else nil)
	Semconv bool     // true when Key is an OTel semconv key reused verbatim
	// Was lists spellings this key used to have. They are not rejected and not
	// rewritten: data carrying them predates the rename and is still readable,
	// so a consumer filtering on history needs both. Empty for a key that never
	// moved.
	Was []string
}

var governanceAttributes = []GovernanceAttribute{
	{
		Key:     "service.namespace",
		Summary: "owning team or service group; reuse the semconv key when the entity is a service and do not override an existing value",
		Example: "checkout",
		Semconv: true,
	},
	{
		Key:     "senhub.owner.team",
		Summary: "owning team for any entity type, where service.namespace does not apply",
		Example: "sre-platform",
		Was:     []string{"entity.owner.team"},
	},
	{
		Key:     "senhub.owner.contact",
		Summary: "escalation contact for the owning team (optional)",
		Example: "sre@acme.io",
		Was:     []string{"entity.owner.contact"},
	},
	{
		Key:     "service.criticality",
		Summary: "business criticality / tier; semconv key (service-scoped upstream) applied to any entity type",
		Example: "high",
		Values:  []string{"critical", "high", "medium", "low"},
		Semconv: true,
	},
	{
		Key:     "senhub.location.site",
		Summary: "physical site or campus (on-prem; semconv covers only cloud regions)",
		Example: "paris",
		Was:     []string{"entity.location.site"},
	},
	{
		Key:     "senhub.location.datacenter",
		Summary: "physical datacenter",
		Example: "dc-eq5",
		Was:     []string{"entity.location.datacenter"},
	},
	{
		Key:     "senhub.location.rack",
		Summary: "physical rack",
		Example: "R12",
		Was:     []string{"entity.location.rack"},
	},
	{
		Key:     "senhub.location.room",
		Summary: "physical room or hall",
		Example: "hall-2",
		Was:     []string{"entity.location.room"},
	},
	{
		Key:     "senhub.lifecycle.status",
		Summary: "operator-asserted lifecycle / maintenance state (open enum)",
		Example: "maintenance",
		Values:  []string{"active", "maintenance", "decommissioning", "retired"},
		Was:     []string{"entity.lifecycle.status"},
	},
}

// GovernanceAttributes returns the advisory cross-cutting governance vocabulary
// as a copy, so callers cannot mutate the registry.
func GovernanceAttributes() []GovernanceAttribute {
	out := make([]GovernanceAttribute, len(governanceAttributes))
	copy(out, governanceAttributes)
	return out
}
