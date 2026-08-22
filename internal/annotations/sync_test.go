package annotations

import (
	"context"
	"testing"
	"time"
)

// memStore is an in-memory ObjectStore: the sync contract only needs
// Put/Get/List, and a map keeps the tests about the sync, not the backend.
type memStore struct{ objects map[string][]byte }

func newMemStore() *memStore { return &memStore{objects: map[string][]byte{}} }

func (m *memStore) Put(_ context.Context, name string, data []byte) error {
	m.objects[name] = append([]byte(nil), data...)
	return nil
}

func (m *memStore) Get(_ context.Context, name string) ([]byte, error) {
	return m.objects[name], nil
}

func (m *memStore) List(_ context.Context, prefix string) ([]string, error) {
	var out []string
	for name := range m.objects {
		if len(name) >= len(prefix) && name[:len(prefix)] == prefix {
			out = append(out, name)
		}
	}
	return out, nil
}

// TestSyncConvergesTwoNodes pins what #348 exists for: two stores that never
// exchange anything meet at the object store and end up identical. This is the
// HA-pair scenario that failed in production — the reboot constraint readable
// on one of the two nodes it protects.
func TestSyncConvergesTwoNodes(t *testing.T) {
	nodeA, nodeB := openTemp(t), openTemp(t)
	remote := newMemStore()
	ctx := context.Background()
	t0 := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	if _, err := nodeA.Set("host:aaa", map[string]string{"ha.pair": "sha002", "ops.reboot.constraint": "never both"}, "op", t0); err != nil {
		t.Fatalf("write on A: %v", err)
	}
	if _, err := nodeB.Set("host:bbb", map[string]string{"owner": "infra"}, "op", t0); err != nil {
		t.Fatalf("write on B: %v", err)
	}

	for _, s := range []*Store{nodeA, nodeB} {
		if _, _, err := SyncOnce(ctx, s, remote, "annotations/sensorfactory"); err != nil {
			t.Fatalf("sync: %v", err)
		}
	}
	// A pushed before B existed remotely; one more pass completes convergence.
	if _, pulled, err := SyncOnce(ctx, nodeA, remote, "annotations/sensorfactory"); err != nil || pulled != 1 {
		t.Fatalf("second pass on A: pulled=%d err=%v", pulled, err)
	}

	for name, s := range map[string]*Store{"A": nodeA, "B": nodeB} {
		if a, ok, _ := s.Get("host:aaa"); !ok || a.Values["ops.reboot.constraint"] != "never both" {
			t.Errorf("node %s: the HA constraint is not readable: ok=%v %v", name, ok, a.Values)
		}
		if a, ok, _ := s.Get("host:bbb"); !ok || a.Values["owner"] != "infra" {
			t.Errorf("node %s: B's annotation did not travel: ok=%v %v", name, ok, a.Values)
		}
	}
}

// TestSyncNewerWriteWins pins last-writer-wins in both directions: the newer
// row replaces the older wholesale, whichever node holds it, and the loser is
// a full row — never a field-level merge of two authors.
func TestSyncNewerWriteWins(t *testing.T) {
	nodeA, nodeB := openTemp(t), openTemp(t)
	remote := newMemStore()
	ctx := context.Background()
	t0 := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	if _, err := nodeA.Set("host:aaa", map[string]string{"owner": "old-team"}, "op", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeB.Set("host:aaa", map[string]string{"owner": "new-team"}, "op", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	for _, s := range []*Store{nodeA, nodeB, nodeA} {
		if _, _, err := SyncOnce(ctx, s, remote, "p"); err != nil {
			t.Fatalf("sync: %v", err)
		}
	}
	if a, ok, _ := nodeA.Get("host:aaa"); !ok || a.Values["owner"] != "new-team" {
		t.Errorf("older write survived on A: %v", a.Values)
	}
	// And the older row must not clobber the winner back on a later pass.
	if _, _, err := SyncOnce(ctx, nodeB, remote, "p"); err != nil {
		t.Fatal(err)
	}
	if a, ok, _ := nodeB.Get("host:aaa"); !ok || a.Values["owner"] != "new-team" {
		t.Errorf("winner clobbered back on B: %v", a.Values)
	}
}

// TestSyncDeletionTravels pins the tombstone half: a removal is a write, so it
// propagates and WINS over the stale value — without tombstones the deleted
// annotation would resurrect from the shared store on the next pull, which is
// the silent failure mode a hard delete bakes in.
func TestSyncDeletionTravels(t *testing.T) {
	nodeA, nodeB := openTemp(t), openTemp(t)
	remote := newMemStore()
	ctx := context.Background()
	t0 := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	if _, err := nodeA.Set("host:aaa", map[string]string{"ops.maintenance": "test window"}, "op", t0); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Store{nodeA, nodeB} {
		if _, _, err := SyncOnce(ctx, s, remote, "p"); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, _ := nodeB.Get("host:aaa"); !ok {
		t.Fatal("fixture: annotation did not reach B")
	}

	// Remove on A (empty value removes the key), then let both sync.
	if _, err := nodeA.Set("host:aaa", map[string]string{"ops.maintenance": ""}, "op", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Store{nodeA, nodeB} {
		if _, _, err := SyncOnce(ctx, s, remote, "p"); err != nil {
			t.Fatal(err)
		}
	}
	if a, ok, _ := nodeB.Get("host:aaa"); ok {
		t.Errorf("deleted annotation still readable on B: %v", a.Values)
	}
	// And it must not resurrect on A either.
	if _, _, err := SyncOnce(ctx, nodeA, remote, "p"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := nodeA.Get("host:aaa"); ok {
		t.Error("deleted annotation resurrected on A from the shared store")
	}
}

// TestSyncKeyRoundTrip pins the naming: any local key — identity fingerprints,
// legacy ULIDs, anything — survives the trip through a backend's name rules,
// and foreign objects under the prefix are skipped rather than wedging the
// sync forever.
func TestSyncKeyRoundTrip(t *testing.T) {
	nodeA, nodeB := openTemp(t), openTemp(t)
	remote := newMemStore()
	ctx := context.Background()
	t0 := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	keys := []string{"host:1a2b3c", "01LEGACYULID", "weird/key with:everything"}
	for _, k := range keys {
		if _, err := nodeA.Set(k, map[string]string{"v": "x"}, "op", t0); err != nil {
			t.Fatal(err)
		}
	}
	remote.objects["p/not-hex.ann"] = []byte("{}")
	remote.objects["p/deadbeef.other"] = []byte("junk")

	if _, _, err := SyncOnce(ctx, nodeA, remote, "p"); err != nil {
		t.Fatalf("sync A: %v", err)
	}
	if _, _, err := SyncOnce(ctx, nodeB, remote, "p"); err != nil {
		t.Fatalf("sync B: %v", err)
	}
	for _, k := range keys {
		if _, ok, _ := nodeB.Get(k); !ok {
			t.Errorf("key %q did not round-trip", k)
		}
	}
}
