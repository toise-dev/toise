package main

import (
	"time"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/registry"
)

// The /metrics surface reports one Toise instance, not one tenant: the existing
// metric names and shapes are preserved by summing each gauge/counter across all
// open tenant stacks (for a single-tenant deployment this equals that tenant's
// values, so dashboards and alerts are unaffected). See #44, #95.

type aggregateGraph struct{ reg *registry.Registry }

func (g aggregateGraph) EntityCount() int {
	n := 0
	for _, st := range g.reg.Stacks() {
		n += st.Graph.EntityCount()
	}
	return n
}

func (g aggregateGraph) RelationCount() int {
	n := 0
	for _, st := range g.reg.Stacks() {
		n += st.Graph.RelationCount()
	}
	return n
}

// EntitiesOfTypePerTenant keeps each tenant's entities in their own slice. The
// serial-overlap check compares entities to one another, and two estates that
// never met may carry the same chassis serial: pooling them would publish a
// cross-tenant comparison as a single number.
func (g aggregateGraph) EntitiesOfTypePerTenant(typ string) [][]model.Entity {
	stacks := g.reg.Stacks()
	out := make([][]model.Entity, 0, len(stacks))
	for _, st := range stacks {
		if ents := st.Graph.EntitiesOfType(typ); len(ents) > 0 {
			out = append(out, ents)
		}
	}
	return out
}

func (g aggregateGraph) CountByType() map[string]int {
	out := make(map[string]int)
	for _, st := range g.reg.Stacks() {
		for typ, c := range st.Graph.CountByType() {
			out[typ] += c
		}
	}
	return out
}

type aggregateStore struct{ reg *registry.Registry }

func (s aggregateStore) Sequence() uint64 {
	var n uint64
	for _, st := range s.reg.Stacks() {
		n += st.Store.Sequence()
	}
	return n
}

func (s aggregateStore) DiskUsage() uint64 {
	var n uint64
	for _, st := range s.reg.Stacks() {
		n += st.Store.DiskUsage()
	}
	return n
}

func (s aggregateStore) PrunedEvents() uint64 {
	var n uint64
	for _, st := range s.reg.Stacks() {
		n += st.Store.PrunedEvents()
	}
	return n
}

func (s aggregateStore) PrunedBytes() uint64 {
	var n uint64
	for _, st := range s.reg.Stacks() {
		n += st.Store.PrunedBytes()
	}
	return n
}

// SnapshotSeq is a per-tenant reference sequence, so the aggregate reports the
// highest across tenants (a high-water mark) rather than a meaningless sum.
func (s aggregateStore) SnapshotSeq() uint64 {
	var highest uint64
	for _, st := range s.reg.Stacks() {
		if seq := st.Store.SnapshotSeq(); seq > highest {
			highest = seq
		}
	}
	return highest
}

func (s aggregateStore) SnapshotsWritten() uint64 {
	var n uint64
	for _, st := range s.reg.Stacks() {
		n += st.Store.SnapshotsWritten()
	}
	return n
}

// PruneHorizon is per-tenant; the aggregate reports the latest cutoff across
// tenants — the most-recently-pruned tenant is what bounds how far back a
// fleet-wide as-of read can answer completely.
func (s aggregateStore) PruneHorizon() time.Time {
	var latest time.Time
	for _, st := range s.reg.Stacks() {
		if h := st.Store.PruneHorizon(); h.After(latest) {
			latest = h
		}
	}
	return latest
}
