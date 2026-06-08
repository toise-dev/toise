package main

import "github.com/toise-dev/toise/internal/registry"

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
