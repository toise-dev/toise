package change

import (
	"log/slog"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

// Within the tombstone grace window, a producer that went silent and returns
// reclaims its original logical id, so entity_history stays continuous instead
// of fragmenting across ULIDs (#183).
func TestResurrectionReclaimsIDWithinGraceWindow(t *testing.T) {
	g := projection.New()
	now := t0
	e := New(g, &fakeAppender{},
		WithClock(func() time.Time { return now }),
		WithLogger(slog.New(slog.DiscardHandler)))

	host := []model.KeyValue{kv("host.id", "h1")}
	observe := func() model.Event {
		t.Helper()
		ev, err := e.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: host, Interval: time.Minute, EventTime: now})
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}

	first := observe()
	if first.Entity.ChangeType != model.EntityCreated {
		t.Fatalf("first sight: ChangeType = %v, want EntityCreated", first.Entity.ChangeType)
	}
	id1 := first.Entity.Entity.ID

	// The producer goes silent past its interval; the sweeper soft-deletes it.
	now = now.Add(90 * time.Second)
	if n, _ := e.Sweep(); n.Total() != 1 {
		t.Fatalf("expected 1 expiry, swept %d", n.Total())
	}
	if _, found := g.MatchIdentity(model.TypeHost, host); found {
		t.Fatal("entity should be soft-deleted after the sweep")
	}

	// It returns well within the grace window: same id, resurrected as a create.
	now = now.Add(2 * time.Minute)
	back := observe()
	if back.Entity.ChangeType != model.EntityCreated {
		t.Fatalf("resurrection: ChangeType = %v, want EntityCreated", back.Entity.ChangeType)
	}
	if got := back.Entity.Entity.ID; got != id1 {
		t.Fatalf("resurrection minted a new id %s, want the original %s", got, id1)
	}
	if _, found := g.MatchIdentity(model.TypeHost, host); !found {
		t.Error("resurrected entity should be live again")
	}
	if g.EntityCount() != 1 {
		t.Errorf("EntityCount = %d, want 1", g.EntityCount())
	}
	// Resurrection must clear the tombstone, not merely reclaim the id: the
	// identity is live, so it is no longer a resurrection candidate (a stale
	// tombByHash/tombDeadline entry left behind would be a leak).
	if _, ok := g.MatchTombstone(model.TypeHost, host); ok {
		t.Error("resurrection should have cleared the tombstone for this identity")
	}
}

// Past the grace window the tombstone is gone, so a returning producer is a
// genuinely new entity and gets a fresh id.
func TestResurrectionMintsNewIDPastGraceWindow(t *testing.T) {
	g := projection.New()
	now := t0
	e := New(g, &fakeAppender{},
		WithClock(func() time.Time { return now }),
		WithLogger(slog.New(slog.DiscardHandler)))

	host := []model.KeyValue{kv("host.id", "h1")}
	observe := func() model.Event {
		t.Helper()
		ev, err := e.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: host, Interval: time.Minute, EventTime: now})
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}

	id1 := observe().Entity.Entity.ID

	now = now.Add(90 * time.Second)
	if n, _ := e.Sweep(); n.Total() != 1 {
		t.Fatalf("expected 1 expiry, swept %d", n.Total())
	}

	// Beyond the 15m grace window; a sweep prunes the stale tombstone.
	now = now.Add(16 * time.Minute)
	_, _ = e.Sweep()

	if got := observe().Entity.Entity.ID; got == id1 {
		t.Fatalf("past the grace window the entity must get a fresh id, reused %s", id1)
	}
	if g.EntityCount() != 1 {
		t.Errorf("EntityCount = %d, want 1", g.EntityCount())
	}
}
