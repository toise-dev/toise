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

// TestDependsOnLocalEndpoint proves the classification and canonicalization the
// helper promises: exactly the four host-local ranges gain the fourth host.id
// key, everything else (RFC1918, CGNAT, public, non-literals) stays 3-key, IPv6
// literals come out in RFC 5952 form with a lowercased verbatim zone, and
// IPv4-mapped literals are unmapped so both spellings derive one identity.
func TestDependsOnLocalEndpoint(t *testing.T) {
	const hostID = "8b861704-05bc-4382-b057-7eac3df5e730"
	cases := []struct {
		name     string
		in       string
		wantAddr string
		wantHost bool
	}{
		{"ipv4 loopback", "127.0.0.1", "127.0.0.1", true},
		{"ipv4 loopback net", "127.53.0.9", "127.53.0.9", true},
		{"ipv6 loopback", "::1", "::1", true},
		{"ipv4 link-local", "169.254.10.20", "169.254.10.20", true},
		{"ipv6 link-local zone lowercased", "FE80::1%ETH0", "fe80::1%eth0", true},
		{"ipv4-mapped loopback unmapped", "::ffff:127.0.0.1", "127.0.0.1", true},
		{"rfc1918 stays 3-key", "10.2.3.4", "10.2.3.4", false},
		{"cgnat stays 3-key", "100.64.0.1", "100.64.0.1", false},
		{"public ipv6 canonicalized 3-key", "2001:DB8:0:0:0:0:0:1", "2001:db8::1", false},
		{"non-literal passes through 3-key", "localhost", "localhost", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rel := DependsOnLocalEndpoint(tc.in, 8080, "tcp", hostID)

			if rel.Type != wire.RelTypeDependsOn || rel.TargetType != wire.TypeNetworkEndpoint {
				t.Fatalf("edge = %q -> %q, want depends_on -> network.endpoint", rel.Type, rel.TargetType)
			}
			if got := rel.TargetID[wire.EndpointServerAddress]; got != tc.wantAddr {
				t.Errorf("server.address = %q, want %q", got, tc.wantAddr)
			}
			if got := rel.TargetID[wire.EndpointServerPort]; got != "8080" {
				t.Errorf("server.port = %q, want string \"8080\"", got)
			}
			if got := rel.TargetID[wire.EndpointNetworkTransport]; got != "tcp" {
				t.Errorf("network.transport = %q, want %q", got, "tcp")
			}
			got, keyed := rel.TargetID[wire.IdentityHostID]
			if keyed != tc.wantHost {
				t.Fatalf("host.id keyed = %v, want %v (TargetID %v)", keyed, tc.wantHost, rel.TargetID)
			}
			if tc.wantHost && got != hostID {
				t.Errorf("host.id = %q, want %q", got, hostID)
			}
			wantLen := 3
			if tc.wantHost {
				wantLen = 4
			}
			if len(rel.TargetID) != wantLen {
				t.Errorf("TargetID has %d keys, want %d: %v", len(rel.TargetID), wantLen, rel.TargetID)
			}
		})
	}
}

// TestDependsOnLocalEndpointBuilds proves the 4-key descriptor survives Build
// onto the wire: the target id map carries host.id alongside the three endpoint
// keys, and no belief attributes ride on the edge.
func TestDependsOnLocalEndpointBuilds(t *testing.T) {
	const hostID = "8b861704-05bc-4382-b057-7eac3df5e730"
	c, _, _ := fixtureClient(t)
	m := descriptor(t, c, DependsOnLocalEndpoint("127.0.0.1", 6379, "tcp", hostID))

	id, ok := m.Get(wire.RelTargetID)
	if !ok {
		t.Fatal("no target id map emitted")
	}
	if v, ok := id.Map().Get(wire.IdentityHostID); !ok || v.Str() != hostID {
		t.Errorf("host.id on the wire = %q, want %q", v.Str(), hostID)
	}
	if v, ok := id.Map().Get(wire.EndpointServerAddress); !ok || v.Str() != "127.0.0.1" {
		t.Errorf("server.address on the wire = %q, want %q", v.Str(), "127.0.0.1")
	}
	if _, ok := m.Get(wire.RelConfidence); ok {
		t.Error("confidence emitted on a depends_on edge (belief rides only on same_as)")
	}
}
