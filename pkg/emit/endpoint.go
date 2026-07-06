package emit

import (
	"strconv"

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
