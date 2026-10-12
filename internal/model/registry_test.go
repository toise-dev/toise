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
