package canonical_test

import (
	"testing"

	"github.com/toise-dev/toise/internal/canonical"
	"github.com/toise-dev/toise/internal/model"
)

// fakeGraph is a hand-built adjacency over same_as edges. The walk only reads
// GetEntity and RelationsTouching, so the projection is not needed to pin its
// semantics.
type fakeGraph struct {
	entities map[model.EntityID]model.Entity
	deleted  map[model.EntityID]bool
	rels     []model.Relation
}

func (f *fakeGraph) GetEntity(id model.EntityID) (model.Entity, bool, bool) {
	e, ok := f.entities[id]
	return e, ok, f.deleted[id]
}

func (f *fakeGraph) RelationsTouching(id model.EntityID, relType string) []model.Relation {
	var out []model.Relation
	for _, r := range f.rels {
		if relType != "" && r.Type != relType {
			continue
		}
		if r.From == id || r.To == id {
			out = append(out, r)
		}
	}
	return out
}

func sameAs(id string, from, to model.EntityID, attrs ...model.KeyValue) model.Relation {
	return model.Relation{ID: model.RelationID(id), Type: model.RelSameAs, From: from, To: to, Attributes: attrs}
}

func conf(v float64) model.KeyValue {
	return model.KeyValue{Key: canonical.ConfidenceKey, Value: model.DoubleValue(v)}
}

func basis(s string) model.KeyValue {
	return model.KeyValue{Key: canonical.BasisKey, Value: model.StringValue(s)}
}

func newGraph(rels ...model.Relation) *fakeGraph {
	g := &fakeGraph{entities: map[model.EntityID]model.Entity{}, deleted: map[model.EntityID]bool{}, rels: rels}
	for _, id := range []model.EntityID{"a", "b", "c", "d"} {
		g.entities[id] = model.Entity{ID: id, Type: model.TypeHost}
	}
	return g
}

func ids(members []model.EntityID) map[model.EntityID]bool {
	out := make(map[model.EntityID]bool, len(members))
	for _, m := range members {
		out[m] = true
	}
	return out
}

// TestWalkTransitive pins the property the overlay exists for: belief composes.
// If a says it is b and b says it is c, then a's group holds c even though no
// producer ever asserted a=c.
func TestWalkTransitive(t *testing.T) {
	g := newGraph(
		sameAs("r1", "a", "b", conf(0.95), basis("hyperv-kvp")),
		sameAs("r2", "b", "c", conf(0.99), basis("serial_match")),
	)
	members, links := canonical.Walk(g, "a", canonical.DefaultThreshold)
	if members[0] != "a" {
		t.Fatalf("members[0] = %q, want the root first", members[0])
	}
	got := ids(members)
	if len(got) != 3 || !got["b"] || !got["c"] {
		t.Fatalf("members = %v, want {a,b,c}", members)
	}
	if len(links) != 2 {
		t.Fatalf("links = %+v, want the two supporting edges", links)
	}
	if links[0].From != "a" || links[1].From != "b" {
		t.Errorf("links not sorted by endpoint: %+v", links)
	}
	if links[0].Basis != "hyperv-kvp" {
		t.Errorf("basis = %q, want the plain value (not the type-tagged encoding)", links[0].Basis)
	}
}

// TestWalkThresholdGate is the conservative half of ADR 0020: a weak belief is
// kept in the graph but collapses nothing. A wrong merge answers confidently
// about the wrong machine, which is worse than a visible gap.
func TestWalkThresholdGate(t *testing.T) {
	g := newGraph(sameAs("r1", "a", "b", conf(0.5), basis("name_match")))
	members, links := canonical.Walk(g, "a", canonical.DefaultThreshold)
	if len(members) != 1 || len(links) != 0 {
		t.Fatalf("members = %v, links = %+v; a 0.5 belief must not collapse at 0.9", members, links)
	}
	// The same edge does collapse for a consumer that lowered the bar.
	if members, _ := canonical.Walk(g, "a", 0.4); len(members) != 2 {
		t.Fatalf("members = %v, want the alias to join at threshold 0.4", members)
	}
}

// TestWalkMalformedConfidence: an unreadable belief is no belief. A missing,
// non-numeric or out-of-range confidence must never merge two machines — the
// failure mode of treating it as a weak-but-present value.
func TestWalkMalformedConfidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		attr []model.KeyValue
	}{
		{"absent", []model.KeyValue{basis("none")}},
		{"non-numeric", []model.KeyValue{{Key: canonical.ConfidenceKey, Value: model.StringValue("0.99")}}},
		{"above one", []model.KeyValue{conf(1.5)}},
		{"negative", []model.KeyValue{conf(-1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGraph(sameAs("r1", "a", "b", tc.attr...))
			if members, _ := canonical.Walk(g, "a", canonical.DefaultThreshold); len(members) != 1 {
				t.Fatalf("members = %v, want a alone", members)
			}
		})
	}
	// An integer 1 is a legitimate certainty, not a malformed double.
	g := newGraph(sameAs("r1", "a", "b", model.KeyValue{Key: canonical.ConfidenceKey, Value: model.IntValue(1)}))
	if members, _ := canonical.Walk(g, "a", canonical.DefaultThreshold); len(members) != 2 {
		t.Fatalf("members = %v, want an integer confidence of 1 to collapse", members)
	}
}

// TestWalkCycleTerminates: producers assert beliefs independently, so a=b, b=c,
// c=a is expected input, not a corruption. Each edge is reported once.
func TestWalkCycleTerminates(t *testing.T) {
	g := newGraph(
		sameAs("r1", "a", "b", conf(0.95)),
		sameAs("r2", "b", "c", conf(0.95)),
		sameAs("r3", "c", "a", conf(0.95)),
	)
	members, links := canonical.Walk(g, "a", canonical.DefaultThreshold)
	if len(members) != 3 {
		t.Fatalf("members = %v, want three", members)
	}
	if len(links) != 3 {
		t.Fatalf("links = %+v, want each edge exactly once", links)
	}
}

// TestWalkReachesRootFromEitherEnd: same_as is symmetric in meaning even though
// the edge is stored directed, so the group is the same whichever end is asked.
func TestWalkReachesRootFromEitherEnd(t *testing.T) {
	g := newGraph(sameAs("r1", "a", "b", conf(0.95)))
	for _, root := range []model.EntityID{"a", "b"} {
		if members, _ := canonical.Walk(g, root, canonical.DefaultThreshold); len(members) != 2 {
			t.Fatalf("from %q: members = %v, want both ends", root, members)
		}
	}
}

// TestWalkIgnoresOtherRelationTypes: only same_as carries the identity belief.
// A runs_on edge between two entities says nothing about them being one thing.
func TestWalkIgnoresOtherRelationTypes(t *testing.T) {
	g := newGraph(model.Relation{ID: "r1", Type: model.RelRunsOn, From: "a", To: "b", Attributes: []model.KeyValue{conf(1)}})
	if members, _ := canonical.Walk(g, "a", canonical.DefaultThreshold); len(members) != 1 {
		t.Fatalf("members = %v, want a alone", members)
	}
}
