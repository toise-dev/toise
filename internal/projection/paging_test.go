package projection

import (
	"fmt"
	"sort"
	"testing"

	"github.com/toise-dev/toise/internal/model"
)

func pagingHosts(g *Graph, n int) []model.EntityID {
	ids := make([]model.EntityID, 0, n)
	for i := 0; i < n; i++ {
		id := model.NewEntityID()
		g.Apply(entityCreated(id, model.TypeHost, kv("host.id", fmt.Sprintf("h%d", i))))
		ids = append(ids, id)
	}
	return ids
}

func pagingCollectEntities(t *testing.T, g *Graph, typ string, limit int) ([]model.Entity, []string) {
	t.Helper()
	var all []model.Entity
	var keys []string
	after := ""
	for {
		page, next, _ := g.PageEntitiesByFingerprint(typ, after, limit)
		for _, e := range page {
			all = append(all, e)
			keys = append(keys, e.IdentityHash())
		}
		if next == "" {
			break
		}
		after = next
	}
	return all, keys
}

func TestPagingEnumeratesEveryEntityExactlyOnce(t *testing.T) {
	for _, size := range []int{1, 7, 40, 1000} {
		t.Run(fmt.Sprintf("pageSize=%d", size), func(t *testing.T) {
			g := New()
			pagingHosts(g, 250)

			got, keys := pagingCollectEntities(t, g, model.TypeHost, size)

			if len(got) != 250 {
				t.Fatalf("got %d entities, want 250", len(got))
			}
			seen := make(map[string]bool, len(got))
			for _, e := range got {
				h := e.IdentityHash()
				if seen[h] {
					t.Fatalf("duplicate entity with hash %q", h)
				}
				seen[h] = true
			}
			for i := 0; i < 250; i++ {
				want := model.Entity{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", fmt.Sprintf("h%d", i))}}.IdentityHash()
				if !seen[want] {
					t.Fatalf("missing entity h%d (hash %q)", i, want)
				}
			}
			for i := 1; i < len(keys); i++ {
				if keys[i] <= keys[i-1] {
					t.Fatalf("keys not strictly increasing: keys[%d]=%q <= keys[%d]=%q", i, keys[i], i-1, keys[i-1])
				}
			}
		})
	}
}

func TestPagingTotalIsTheWholeSetOnEveryPage(t *testing.T) {
	g := New()
	pagingHosts(g, 250)

	after := ""
	pages := 0
	for {
		page, next, total := g.PageEntitiesByFingerprint(model.TypeHost, after, 40)
		if total != 250 {
			t.Fatalf("page %d: total = %d, want 250", pages, total)
		}
		pages++
		if next == "" {
			if len(page) == 0 {
				t.Fatalf("last page is empty but total is 250")
			}
			break
		}
		after = next
	}
	if pages != 7 {
		t.Fatalf("got %d pages, want 7", pages)
	}
}

func TestPagingSurvivesTheDeletionOfItsCursorEntity(t *testing.T) {
	g := New()
	pagingHosts(g, 250)

	first, next, _ := g.PageEntitiesByFingerprint(model.TypeHost, "", 40)
	if next == "" {
		t.Fatalf("first page returned empty next cursor")
	}
	seen := make(map[string]bool)
	for _, e := range first {
		seen[e.IdentityHash()] = true
	}

	id, ok := g.ResolveFingerprint(next)
	if !ok {
		t.Fatalf("cursor %q does not resolve to an entity id", next)
	}
	// A cursor that no longer resolves must not be read as end-of-list.
	g.Apply(model.Event{Entity: &model.EntityEvent{
		ChangeType: model.EntityDeleted,
		Entity:     model.Entity{ID: id, Type: model.TypeHost},
	}})

	second, next2, total := g.PageEntitiesByFingerprint(model.TypeHost, next, 40)
	if len(second) == 0 {
		t.Fatalf("page after deleted cursor is empty, enumeration stopped")
	}
	if total != 249 {
		t.Fatalf("total = %d, want 249 after deletion", total)
	}
	for _, e := range second {
		if seen[e.IdentityHash()] {
			t.Fatalf("entity %q already seen before the deleted cursor", e.IdentityHash())
		}
	}
	_ = next2
}

func TestPagingRefusesToInventAPageWhenNoneIsAsked(t *testing.T) {
	g := New()
	pagingHosts(g, 250)

	for _, limit := range []int{0, -1} {
		page, next, total := g.PageEntitiesByFingerprint(model.TypeHost, "", limit)
		if len(page) != 0 {
			t.Fatalf("limit=%d: got %d entities, want 0", limit, len(page))
		}
		if next != "" {
			t.Fatalf("limit=%d: next = %q, want empty", limit, next)
		}
		if total != 250 {
			t.Fatalf("limit=%d: total = %d, want 250", limit, total)
		}
	}
}

func TestPagingFiltersByType(t *testing.T) {
	g := New()
	pagingHosts(g, 30)
	for i := 0; i < 20; i++ {
		g.Apply(entityCreated(model.NewEntityID(), model.TypeProcess, kv("process.id", fmt.Sprintf("p%d", i))))
	}

	hosts, _ := pagingCollectEntities(t, g, model.TypeHost, 7)
	if len(hosts) != 30 {
		t.Fatalf("hosts: got %d, want 30", len(hosts))
	}
	for _, e := range hosts {
		if e.Type != model.TypeHost {
			t.Fatalf("got entity of type %q while filtering hosts", e.Type)
		}
	}
	_, _, totalHosts := g.PageEntitiesByFingerprint(model.TypeHost, "", 5)
	if totalHosts != 30 {
		t.Fatalf("total hosts = %d, want 30", totalHosts)
	}

	procs, _ := pagingCollectEntities(t, g, model.TypeProcess, 7)
	if len(procs) != 20 {
		t.Fatalf("processes: got %d, want 20", len(procs))
	}
	for _, e := range procs {
		if e.Type != model.TypeProcess {
			t.Fatalf("got entity of type %q while filtering processes", e.Type)
		}
	}
	_, _, totalProcs := g.PageEntitiesByFingerprint(model.TypeProcess, "", 5)
	if totalProcs != 20 {
		t.Fatalf("total processes = %d, want 20", totalProcs)
	}

	_, _, totalAll := g.PageEntitiesByFingerprint("", "", 5)
	if totalAll != 50 {
		t.Fatalf("total without type = %d, want 50", totalAll)
	}
}

func pagingBuildRelationsGraph(t *testing.T) (*Graph, []model.EntityID, []model.EntityID) {
	t.Helper()
	g := New()
	hosts := pagingHosts(g, 20)
	procs := make([]model.EntityID, 0, 60)
	for i := 0; i < 60; i++ {
		pid := model.NewEntityID()
		g.Apply(entityCreated(pid, model.TypeProcess, kv("process.id", fmt.Sprintf("p%d", i))))
		procs = append(procs, pid)
		rel := model.NewRelation(model.RelRunsOn, pid, hosts[i%20])
		g.Apply(model.Event{Relation: &model.RelationEvent{ChangeType: model.RelationAdded, Relation: rel}})
	}
	return g, hosts, procs
}

func TestPageRelationsByEndpointMatchesTheFullScan(t *testing.T) {
	g, hosts, procs := pagingBuildRelationsGraph(t)

	hostID := hosts[3]
	var paged []model.Relation
	after := ""
	for {
		page, next, _ := g.PageRelations("", "", hostID, after, 5)
		paged = append(paged, page...)
		if next == "" {
			break
		}
		after = next
	}

	var want []model.Relation
	all, _, _ := g.PageRelations("", "", "", "", 1000)
	for _, r := range all {
		if r.To == hostID {
			want = append(want, r)
		}
	}
	if len(paged) != len(want) {
		t.Fatalf("paged %d relations to host, full scan found %d", len(paged), len(want))
	}
	gotIDs := make(map[model.RelationID]bool)
	for _, r := range paged {
		gotIDs[r.ID] = true
	}
	for _, r := range want {
		if !gotIDs[r.ID] {
			t.Fatalf("relation %q missing from paged enumeration", string(r.ID))
		}
	}

	procID := procs[7]
	var pagedFrom []model.Relation
	after = ""
	for {
		page, next, _ := g.PageRelations("", procID, "", after, 5)
		pagedFrom = append(pagedFrom, page...)
		if next == "" {
			break
		}
		after = next
	}
	count := 0
	for _, r := range all {
		if r.From == procID {
			count++
		}
	}
	if len(pagedFrom) != count {
		t.Fatalf("paged %d relations from proc, full scan found %d", len(pagedFrom), count)
	}
}

func TestPageRelationsEnumeratesEveryRelationExactlyOnce(t *testing.T) {
	g, _, _ := pagingBuildRelationsGraph(t)

	var all []model.Relation
	var keys []string
	after := ""
	for {
		page, next, total := g.PageRelations("", "", "", after, 9)
		if total != 60 {
			t.Fatalf("total = %d, want 60", total)
		}
		for _, r := range page {
			all = append(all, r)
			keys = append(keys, string(r.ID))
		}
		if next == "" {
			break
		}
		after = next
	}

	if len(all) != 60 {
		t.Fatalf("got %d relations, want 60", len(all))
	}
	seen := make(map[string]bool)
	for _, r := range all {
		k := string(r.ID)
		if seen[k] {
			t.Fatalf("duplicate relation %q", k)
		}
		seen[k] = true
	}
	sort.Strings(keys)
	for i := 1; i < len(keys); i++ {
		if keys[i] <= keys[i-1] {
			t.Fatalf("keys not strictly increasing at %d: %q <= %q", i, keys[i], keys[i-1])
		}
	}
}
