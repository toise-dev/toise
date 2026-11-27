package mcp

import (
	"sort"
	"strings"

	"github.com/toise-dev/toise/internal/model"
)

// ProducerConflicts reports, for a producer identity (service.instance.id), the distinct host.id values that reported under it recently; nil/empty when fewer than two.
type ProducerConflicts interface {
	ConflictingHosts(producer string) []string
}

// SetProducerConflicts stores the conflict lookup and returns the server for chaining.
func (s *Server) SetProducerConflicts(c ProducerConflicts) *Server {
	s.conflicts = c
	return s
}

type ProducerConflictOut struct {
	Message string   `json:"message" jsonschema:"One sentence explaining that multiple machines share this producer identity and why their reported edges and attributes cannot be attributed to a single machine."`
	HostIDs []string `json:"host_ids" jsonschema:"The distinct host.id values currently reporting under this service.instance.id, which is what makes the identity ambiguous."`
}

func (s *Server) producerConflictFor(e model.Entity, asOf string) *ProducerConflictOut {
	if s.conflicts == nil || asOf != "" || e.Type != model.TypeServiceInstance {
		return nil
	}
	var producer string
	for _, kv := range e.Identity {
		if kv.Key == "service.instance.id" && kv.Value.Str() != "" {
			producer = kv.Value.Str()
			break
		}
	}
	if producer == "" {
		return nil
	}
	hosts := s.conflicts.ConflictingHosts(producer)
	if len(hosts) < 2 {
		return nil
	}
	sorted := append([]string(nil), hosts...)
	sort.Strings(sorted)
	return &ProducerConflictOut{
		Message: "Several machines report under this service.instance.id (host.id: " +
			strings.Join(sorted, ", ") +
			"), so this identity is shared (a copied agent key or a cloned image): their edges are tracked separately and none is retracted on behalf of another, and attributes written by the last exporter may belong to either machine.",
		HostIDs: sorted,
	}
}
