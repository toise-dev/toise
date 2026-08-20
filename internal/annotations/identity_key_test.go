package annotations

import (
	"testing"
	"time"
)

// TestGetAtMigratesFromLegacyKey pins the lazy migration: a row written under
// the old logical-id scheme is found on first read, moved to the identity key,
// and the old key is gone afterwards. Without this, every annotation written
// before the re-keying would silently disappear on upgrade.
func TestGetAtMigratesFromLegacyKey(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	if _, err := s.Set("01LEGACYULID", map[string]string{"owner": "infra"}, "op", now); err != nil {
		t.Fatalf("seeding legacy row: %v", err)
	}

	a, ok, err := s.GetAt("host:abc123", "01LEGACYULID")
	if err != nil {
		t.Fatalf("GetAt: %v", err)
	}
	if !ok || a.Values["owner"] != "infra" {
		t.Fatalf("legacy annotation not found through GetAt: ok=%v values=%v", ok, a.Values)
	}

	// It now lives under the identity key...
	if _, ok, _ := s.Get("host:abc123"); !ok {
		t.Error("annotation was not written under the identity key")
	}
	// ...and no longer under the old one, so it cannot be found twice.
	if _, ok, _ := s.Get("01LEGACYULID"); ok {
		t.Error("legacy row survived the migration; the annotation now exists twice")
	}
}

// TestSetAtMergesOntoMigratedValues proves a write does not silently discard
// what an operator wrote before the re-keying. Merging is the documented
// behavior of Set, and it must hold across the key change too.
func TestSetAtMergesOntoMigratedValues(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	if _, err := s.Set("01LEGACYULID", map[string]string{"owner": "infra"}, "op", now); err != nil {
		t.Fatalf("seeding legacy row: %v", err)
	}

	a, err := s.SetAt("host:abc123", "01LEGACYULID", map[string]string{"runbook": "https://example/rb"}, "op2", now)
	if err != nil {
		t.Fatalf("SetAt: %v", err)
	}
	if a.Values["owner"] != "infra" {
		t.Errorf("pre-existing key lost across the key change: %v", a.Values)
	}
	if a.Values["runbook"] == "" {
		t.Errorf("new key not written: %v", a.Values)
	}
}

// TestGetAtIsANoOpWithoutALegacyKey keeps the common path cheap and total:
// once everything is identity-keyed, a miss must stay a miss rather than
// resolving to something else.
func TestGetAtIsANoOpWithoutALegacyKey(t *testing.T) {
	s := openTemp(t)

	if _, ok, err := s.GetAt("host:abc123", ""); err != nil || ok {
		t.Fatalf("empty legacy key should be a plain miss, got ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.GetAt("host:abc123", "host:abc123"); err != nil || ok {
		t.Fatalf("identical keys should be a plain miss, got ok=%v err=%v", ok, err)
	}
}

// TestIdentityKeyIsReplicaIndependent is the property the whole change exists
// for: two nodes that never exchanged anything derive the same key for the same
// thing, because the key comes from the identifying attributes and not from a
// per-node id. The fingerprint itself is pinned in the model package; here we
// assert the store treats it as an ordinary key, so two nodes agree by
// construction.
func TestIdentityKeyIsReplicaIndependent(t *testing.T) {
	nodeA, nodeB := openTemp(t), openTemp(t)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	const identity = "host:1a2b3c4d5e6f"

	if _, err := nodeA.SetAt(identity, "01ULID_ON_A", map[string]string{"ha.pair": "sha002"}, "op", now); err != nil {
		t.Fatalf("write on node A: %v", err)
	}
	// Node B knows the same machine under a different logical id.
	if _, err := nodeB.SetAt(identity, "01ULID_ON_B", map[string]string{"ha.pair": "sha002"}, "op", now); err != nil {
		t.Fatalf("write on node B: %v", err)
	}

	a, okA, _ := nodeA.Get(identity)
	b, okB, _ := nodeB.Get(identity)
	if !okA || !okB {
		t.Fatalf("identity key not resolvable on both nodes: A=%v B=%v", okA, okB)
	}
	if a.Values["ha.pair"] != b.Values["ha.pair"] {
		t.Errorf("same identity, different annotation: %v vs %v", a.Values, b.Values)
	}
}

// openTemp returns a store on a throwaway directory, closed with the test.
func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
