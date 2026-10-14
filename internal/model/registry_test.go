package model

import "testing"

// TestTerminalTypesAreRegistered keeps the terminal set honest: a terminal type
// that is not a registered entity type would silently never match, and the
// traversal guard it exists for (#442) would be inert.
func TestTerminalTypesAreRegistered(t *testing.T) {
	for typ := range terminalEntityTypes {
		if !IsKnownEntityType(typ) {
			t.Errorf("terminal type %q is not a registered entity type", typ)
		}
	}
	if !IsTerminalEntityType(TypeNetworkEndpoint) {
		t.Error("an observed network.endpoint is terminal by ADR 0032 and must stay so")
	}
	if IsTerminalEntityType(TypeHost) {
		t.Error("a host is a thing entities are part of, not a destination: it must stay traversable")
	}
}

// TestObservationRelationsAreNotFailurePaths pins a class decision rather than
// four values: a relation that records what something OBSERVED or BELIEVES is
// not a path a failure travels along.
//
// monitors held ImpactToFrom for three releases, which said a monitored host
// going down degrades the agent watching it. It does not: the agent is healthy
// and its reading is bad. Left alone it would have made every probe target a
// failure path into its own agent, so one unreachable target inflated the blast
// radius by the whole fleet that agent watches — and the more probes shipped,
// the wider the error.
func TestObservationRelationsAreNotFailurePaths(t *testing.T) {
	observational := map[string]string{
		RelSameAs:         "an alias assertion is a belief about identity, not a dependency",
		RelResolvesTo:     "a neighbor resolving to an interface is a reading, not a reliance",
		RelAggregatedInto: "a member folded into an aggregate is a presentation of the same facts",
		RelMonitors:       "watching a thing is not depending on it",
	}
	for relType, why := range observational {
		def, ok := RelationDef(relType)
		if !ok {
			t.Fatalf("%s is not registered: the invariant cannot be checked, and a relation absent from the registry propagates impact BOTH ways by default", relType)
		}
		if def.Impact != ImpactNone {
			t.Errorf("%s has Impact %v, want ImpactNone: %s. A failure must not travel this edge; if this relation really is a dependency, it does not belong in this list.", relType, def.Impact, why)
		}
	}
}
