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
// The entity.* keys are Toise-provisional where semconv is silent — candidates
// to raise with the OpenTelemetry Resources & Entities SIG.
type GovernanceAttribute struct {
	Key     string   // the attribute key producers should use
	Summary string   // one-line meaning, surfaced to consumers
	Example string   // an example value, to show the shape
	Values  []string // well-known values when the key is an open enum (else nil)
	Semconv bool     // true when Key is an OTel semconv key reused verbatim
}

var governanceAttributes = []GovernanceAttribute{
	{
		Key:     "service.namespace",
		Summary: "owning team or service group; reuse the semconv key when the entity is a service and do not override an existing value",
		Example: "checkout",
		Semconv: true,
	},
	{
		Key:     "entity.owner.team",
		Summary: "owning team for any entity type, where service.namespace does not apply",
		Example: "sre-platform",
	},
	{
		Key:     "entity.owner.contact",
		Summary: "escalation contact for the owning team (optional)",
		Example: "sre@acme.io",
	},
	{
		Key:     "service.criticality",
		Summary: "business criticality / tier; semconv key (service-scoped upstream) applied to any entity type",
		Example: "high",
		Values:  []string{"critical", "high", "medium", "low"},
		Semconv: true,
	},
	{
		Key:     "entity.location.site",
		Summary: "physical site or campus (on-prem; semconv covers only cloud regions)",
		Example: "paris",
	},
	{
		Key:     "entity.location.datacenter",
		Summary: "physical datacenter",
		Example: "dc-eq5",
	},
	{
		Key:     "entity.location.rack",
		Summary: "physical rack",
		Example: "R12",
	},
	{
		Key:     "entity.location.room",
		Summary: "physical room or hall",
		Example: "hall-2",
	},
	{
		Key:     "entity.lifecycle.status",
		Summary: "operator-asserted lifecycle / maintenance state (open enum)",
		Example: "maintenance",
		Values:  []string{"active", "maintenance", "decommissioning", "retired"},
	},
}

// GovernanceAttributes returns the advisory cross-cutting governance vocabulary
// as a copy, so callers cannot mutate the registry.
func GovernanceAttributes() []GovernanceAttribute {
	out := make([]GovernanceAttribute, len(governanceAttributes))
	copy(out, governanceAttributes)
	return out
}
