package emit

import (
	"net/netip"
	"strconv"
	"strings"

	"github.com/toise-dev/toise/pkg/emit/wire"
)

// DependsOnEndpoint builds the durable dependency relationship a producer asserts
// toward a remote endpoint it observed itself connecting to — the outbound,
// client-side edge of the connection-topology model (ADR 0032). Attach it to the
// emitting entity's Relationships (the source is the entity that carries it).
//
// The target is the observable network.endpoint entity, keyed on the RESOLVED
// {server.address, server.port, network.transport}. Passing the resolved peer (what
// the socket or a config-resolving producer actually reached), never an unresolved
// name/URL, is what makes both ends of a hop derive the same identity — the
// continuity invariant that lets this edge join the remote side's own emission.
// serverPort is written in its string form to match exact-match identity, and
// transport ("tcp"/"udp") is required because it is part of the endpoint identity.
func DependsOnEndpoint(serverAddress string, serverPort int, transport string) Relationship {
	return Relationship{
		Type:       wire.RelTypeDependsOn,
		TargetType: wire.TypeNetworkEndpoint,
		TargetID: map[string]string{
			wire.EndpointServerAddress:    serverAddress,
			wire.EndpointServerPort:       strconv.Itoa(serverPort),
			wire.EndpointNetworkTransport: transport,
		},
	}
}

// DependsOnLocalEndpoint is DependsOnEndpoint for a peer that may be host-local.
// A host-local or link-local address (127.0.0.0/8, ::1, 169.254.0.0/16,
// fe80::/10) has a scope narrower than the observation domain, so the 3-key
// identity would falsely merge distinct endpoints across machines; the contract
// gives such an endpoint a fourth identity key, host.id — the OBSERVING host's
// id, byte-identical to the host entity's identity (ADR 0032 addendum).
//
// The helper classifies the address itself: within those ranges it emits the
// 4-key identity; anywhere else — including RFC1918 and CGNAT, which stay
// 3-key — hostID is dropped and the edge is exactly DependsOnEndpoint's. A
// caller can therefore route every resolved peer through it and never emit a
// key set the contract forbids.
//
// The address is canonicalized on the way through: an IPv6 literal is rendered
// in RFC 5952 text form with its zone index kept verbatim lowercased, and an
// IPv4-mapped IPv6 literal (::ffff:127.0.0.1) is unmapped to its IPv4 form so
// both spellings of one peer derive one identity. A string that is not an IP
// literal passes through untouched and stays 3-key.
func DependsOnLocalEndpoint(serverAddress string, serverPort int, transport, hostID string) Relationship {
	addr, err := netip.ParseAddr(serverAddress)
	if err != nil {
		return DependsOnEndpoint(serverAddress, serverPort, transport)
	}
	addr = addr.Unmap()
	if zone := addr.Zone(); zone != "" {
		addr = addr.WithZone(strings.ToLower(zone))
	}
	rel := DependsOnEndpoint(addr.String(), serverPort, transport)
	if addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		rel.TargetID[wire.IdentityHostID] = hostID
	}
	return rel
}
