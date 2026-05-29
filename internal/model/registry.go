package model

// Phase-1 entity types. See ADR 0004. New types are added here without breaking
// existing ones.
const (
	TypeHost             = "host"
	TypeProcess          = "process"
	TypeNetworkInterface = "network.interface"
	TypeNetworkAddress   = "network.address"
	TypeNetworkRoute     = "network.route"
	TypeServiceListener  = "service.listener"
)

// Phase-1 relation types. See ADR 0004.
const (
	RelRunsOn       = "runs_on"
	RelHasInterface = "has_interface"
	RelBoundTo      = "bound_to"
	RelNextHopVia   = "next_hop_via"
	RelListensOn    = "listens_on"
)

// RelationTypeDef describes a known relation type and its constraints.
type RelationTypeDef struct {
	Type string
	// From and To are the entity types the relation connects.
	From string
	To   string
	// Structural marks whether the relation's appearance/disappearance is
	// significant (suitable for alerting). See ADR 0006.
	Structural bool
}

var entityTypes = map[string]struct{}{
	TypeHost:             {},
	TypeProcess:          {},
	TypeNetworkInterface: {},
	TypeNetworkAddress:   {},
	TypeNetworkRoute:     {},
	TypeServiceListener:  {},
}

var relationTypes = map[string]RelationTypeDef{
	RelRunsOn:       {Type: RelRunsOn, From: TypeProcess, To: TypeHost, Structural: true},
	RelHasInterface: {Type: RelHasInterface, From: TypeHost, To: TypeNetworkInterface, Structural: true},
	RelBoundTo:      {Type: RelBoundTo, From: TypeNetworkAddress, To: TypeNetworkInterface, Structural: true},
	RelNextHopVia:   {Type: RelNextHopVia, From: TypeNetworkRoute, To: TypeNetworkAddress, Structural: true},
	RelListensOn:    {Type: RelListensOn, From: TypeServiceListener, To: TypeNetworkInterface, Structural: true},
}

// IsKnownEntityType reports whether t is a registered entity type.
func IsKnownEntityType(t string) bool {
	_, ok := entityTypes[t]
	return ok
}

// RelationDef returns the definition of relation type t, if registered.
func RelationDef(t string) (RelationTypeDef, bool) {
	d, ok := relationTypes[t]
	return d, ok
}

// EntityTypes returns the registered entity types (unordered).
func EntityTypes() []string {
	out := make([]string, 0, len(entityTypes))
	for t := range entityTypes {
		out = append(out, t)
	}
	return out
}

// RelationTypes returns the registered relation type definitions (unordered).
func RelationTypes() []RelationTypeDef {
	out := make([]RelationTypeDef, 0, len(relationTypes))
	for _, d := range relationTypes {
		out = append(out, d)
	}
	return out
}
