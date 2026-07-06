package emit

import (
	"testing"

	"github.com/toise-dev/toise/pkg/emit/wire"
)

// TestDependsOnEndpoint proves the helper keys the target exactly as the
// connection-topology model requires (ADR 0032): a network.endpoint whose identity
// is the resolved {server.address, server.port, network.transport}, with the port
// in its string form. Keying it any other way is the divergence that breaks the
// continuity join, so this is pinned.
func TestDependsOnEndpoint(t *testing.T) {
	rel := DependsOnEndpoint("10.2.3.4", 23560, "tcp")

	if rel.Type != wire.RelTypeDependsOn {
		t.Errorf("Type = %q, want %q", rel.Type, wire.RelTypeDependsOn)
	}
	if rel.TargetType != wire.TypeNetworkEndpoint {
		t.Errorf("TargetType = %q, want %q", rel.TargetType, wire.TypeNetworkEndpoint)
	}
	want := map[string]string{
		wire.EndpointServerAddress:    "10.2.3.4",
		wire.EndpointServerPort:       "23560",
		wire.EndpointNetworkTransport: "tcp",
	}
	if len(rel.TargetID) != len(want) {
		t.Fatalf("TargetID = %v, want %v", rel.TargetID, want)
	}
	for k, v := range want {
		if rel.TargetID[k] != v {
			t.Errorf("TargetID[%q] = %q, want %q", k, rel.TargetID[k], v)
		}
	}
}

// TestDependsOnEndpointBuilds proves the helper's output is a complete descriptor
// that Build accepts and emits on the wire (Type, TargetType, TargetID all present),
// carrying no belief attributes since it is not a same_as edge.
func TestDependsOnEndpointBuilds(t *testing.T) {
	c, _, _ := fixtureClient(t)
	m := descriptor(t, c, DependsOnEndpoint("10.2.3.4", 23560, "tcp"))

	if rt, _ := m.Get(wire.RelType); rt.Str() != wire.RelTypeDependsOn {
		t.Errorf("relationship.type = %q, want %q", rt.Str(), wire.RelTypeDependsOn)
	}
	if tt, _ := m.Get(wire.RelTargetType); tt.Str() != wire.TypeNetworkEndpoint {
		t.Errorf("target type = %q, want %q", tt.Str(), wire.TypeNetworkEndpoint)
	}
	id, ok := m.Get(wire.RelTargetID)
	if !ok {
		t.Fatal("no target id map emitted")
	}
	port, ok := id.Map().Get(wire.EndpointServerPort)
	if !ok || port.Str() != "23560" {
		t.Errorf("server.port = %q, want string \"23560\"", port.Str())
	}
	if _, ok := m.Get(wire.RelConfidence); ok {
		t.Error("confidence emitted on a depends_on edge (belief rides only on same_as)")
	}
}
