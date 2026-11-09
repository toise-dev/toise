package graphql_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
)

const entitiesPageQuery = `query($first:Int!, $after:String){
	entities(filter:{type:"host"}, first:$first, after:$after){
		totalCount
		pageInfo{ hasNextPage endCursor }
		edges{ cursor node{ id identityFingerprint } }
	}
}`

type entitiesResponse struct {
	Entities struct {
		TotalCount int
		PageInfo   struct {
			HasNextPage bool
			EndCursor   *string
		}
		Edges []struct {
			Cursor string
			Node   struct {
				ID                  string
				IdentityFingerprint string
			}
		}
	}
}

func createHosts(t *testing.T, s *stack, n int) map[string]string {
	t.Helper()
	hostIDByEntityID := make(map[string]string, n)
	for i := 0; i < n; i++ {
		hostID := fmt.Sprintf("hx%03d", i)
		ev, err := s.engine.ObserveEntity(change.EntityObservation{
			Type:       model.TypeHost,
			Identity:   []model.KeyValue{kv("host.id", hostID)},
			Attributes: []model.KeyValue{kv("status", "up")},
			EventTime:  time.Now(),
		})
		if err != nil {
			t.Fatalf("ObserveEntity(%s) failed: %v", hostID, err)
		}
		hostIDByEntityID[fmt.Sprint(ev.Entity.Entity.ID)] = hostID
	}
	return hostIDByEntityID
}

func fetchPage(t *testing.T, s *stack, first int, after *string) entitiesResponse {
	t.Helper()
	c := s.client(t)
	var resp entitiesResponse
	vars := []client.Option{client.Var("first", first)}
	if after != nil {
		vars = append(vars, client.Var("after", *after))
	}
	c.MustPost(entitiesPageQuery, &resp, vars...)
	return resp
}

// A truncated list that reads as complete is the defect this test forbids:
// it must fail if pagination falls back to an exact equality on the cursor's
// local identifier.
func TestEntitiesPagingSurvivesTheDeletionOfItsCursorEntity(t *testing.T) {
	s := newStack(t)
	hostIDByEntityID := createHosts(t, s, 60)

	first := fetchPage(t, s, 20, nil)
	if len(first.Entities.Edges) != 20 {
		t.Fatalf("page 1: got %d edges, want 20", len(first.Entities.Edges))
	}
	if first.Entities.TotalCount != 61 {
		t.Fatalf("page 1: got totalCount = %d, want 61", first.Entities.TotalCount)
	}
	if first.Entities.PageInfo.EndCursor == nil {
		t.Fatal("page 1: endCursor is nil, want a cursor")
	}

	seen := make(map[string]bool, 61)
	for _, e := range first.Entities.Edges {
		seen[e.Node.IdentityFingerprint] = true
	}

	last := first.Entities.Edges[len(first.Entities.Edges)-1]
	hostID, ok := hostIDByEntityID[last.Node.ID]
	if !ok {
		t.Fatalf("cursor entity %q: no known host.id for entity id %q", last.Node.IdentityFingerprint, last.Node.ID)
	}
	if _, _, err := s.engine.DeleteEntity(change.EntityObservation{
		Type:      model.TypeHost,
		Identity:  []model.KeyValue{kv("host.id", hostID)},
		EventTime: time.Now(),
	}); err != nil {
		t.Fatalf("DeleteEntity(%s) failed: %v", hostID, err)
	}

	second := fetchPage(t, s, 20, first.Entities.PageInfo.EndCursor)
	if len(second.Entities.Edges) == 0 {
		t.Fatal("page 2: got no edges, want a non-empty page after deleting the cursor entity")
	}
	if !second.Entities.PageInfo.HasNextPage {
		t.Fatal("page 2: hasNextPage = false, want true")
	}
	for _, e := range second.Entities.Edges {
		if seen[e.Node.IdentityFingerprint] {
			t.Fatalf("page 2: fingerprint %q already seen on page 1", e.Node.IdentityFingerprint)
		}
	}
	if second.Entities.TotalCount != 60 {
		t.Fatalf("page 2: got totalCount = %d, want 60 (61 - 1 deleted)", second.Entities.TotalCount)
	}
}

func TestEntitiesPagingEnumeratesEveryEntityOnce(t *testing.T) {
	s := newStack(t)
	createHosts(t, s, 60)

	seen := make(map[string]bool)
	var prevFingerprint string
	var prevCursor string
	totalCount := -1
	for i := 0; i < 100; i++ {
		var after *string
		if i > 0 {
			after = &prevCursor
		}
		page := fetchPage(t, s, 7, after)
		if totalCount == -1 {
			totalCount = page.Entities.TotalCount
		} else if page.Entities.TotalCount != totalCount {
			t.Fatalf("page %d: totalCount = %d, want %d (constant across pages)", i+1, page.Entities.TotalCount, totalCount)
		}
		if page.Entities.PageInfo.EndCursor == nil {
			t.Fatalf("page %d: endCursor is nil", i+1)
		}
		// The cursor is opaque: its lexicographic order is not guaranteed, so ordering must be asserted on identityFingerprint instead.
		for _, e := range page.Entities.Edges {
			if e.Node.IdentityFingerprint <= prevFingerprint {
				t.Fatalf("page %d: fingerprint %q not strictly greater than previous %q", i+1, e.Node.IdentityFingerprint, prevFingerprint)
			}
			prevFingerprint = e.Node.IdentityFingerprint
			if seen[e.Node.IdentityFingerprint] {
				t.Fatalf("fingerprint %q returned twice", e.Node.IdentityFingerprint)
			}
			seen[e.Node.IdentityFingerprint] = true
		}
		prevCursor = *page.Entities.PageInfo.EndCursor
		if !page.Entities.PageInfo.HasNextPage {
			break
		}
		if i == 99 {
			t.Fatalf("pagination did not finish after 100 pages: unbounded loop, the targeted defect is masked")
		}
	}
	if len(seen) != totalCount {
		t.Fatalf("got %d distinct fingerprints, totalCount = %d", len(seen), totalCount)
	}
}
