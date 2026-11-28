package main

import (
	"github.com/toise-dev/toise/internal/metrics"
	"github.com/toise-dev/toise/internal/registry"
)

// producerLastSeen builds the collector for producer last-seen series. The
// tenant label is what lets the scoped /metrics handler filter these series
// per token, so it must come from the stack, not the engine entry.
func producerLastSeen(reg *registry.Registry) func() []metrics.ProducerSeen {
	return func() []metrics.ProducerSeen {
		var out []metrics.ProducerSeen
		for _, s := range reg.Stacks() {
			if s.Engine == nil {
				continue
			}
			for _, p := range s.Engine.ProducerLastSeen() {
				out = append(out, metrics.ProducerSeen{
					Tenant:   s.Tenant,
					Producer: p.Producer,
					Scope:    p.Scope,
					LastSeen: p.LastSeen,
				})
			}
		}
		return out
	}
}
