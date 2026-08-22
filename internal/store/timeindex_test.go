package store

import (
	"context"
	"testing"

	"github.com/cockroachdb/pebble"

	"github.com/toise-dev/toise/internal/model"
)

// TestScanTimeIndexClassifiesWithoutResolving pins the property #351 exists
// for: a windowed scan learns each event's class from the index entry alone.
// The assertions read ONLY what the scan yields — if classification ever
// regressed to requiring resolution, Tagged would be false and this fails.
func TestScanTimeIndexClassifiesWithoutResolving(t *testing.T) {
	s := newTestStore(t)
	if err := s.Append(
		mkEntityEvent("h1", model.EntityCreated, ts(1)),
		mkEntityEvent("h1", model.EntityUnchanged, ts(2)),
		mkRelationEvent("h1", "h2", ts(3)),
		mkEntityEvent("h1", model.EntityDeleted, ts(4)),
	); err != nil {
		t.Fatalf("append: %v", err)
	}

	var got []TimeIndexEntry
	if err := s.ScanTimeIndex(context.Background(), ts(0), ts(10), false, func(e TimeIndexEntry) error {
		got = append(got, e)
		return nil
	}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("scanned %d entries, want 4", len(got))
	}
	want := []struct {
		ct         model.ChangeType
		structural bool
	}{
		{model.EntityCreated, false},
		{model.EntityUnchanged, false},
		{model.RelationAdded, true}, // runs_on is structural
		{model.EntityDeleted, false},
	}
	for i, w := range want {
		if !got[i].Tagged {
			t.Errorf("entry %d untagged; every fresh write must carry its class", i)
		}
		if got[i].ChangeType != w.ct || got[i].Structural != w.structural {
			t.Errorf("entry %d classified %v/structural=%v, want %v/%v",
				i, got[i].ChangeType, got[i].Structural, w.ct, w.structural)
		}
	}

	// Newest-first is the shape the change tools consume.
	var rev []model.ChangeType
	if err := s.ScanTimeIndex(context.Background(), ts(0), ts(10), true, func(e TimeIndexEntry) error {
		rev = append(rev, e.ChangeType)
		return nil
	}); err != nil {
		t.Fatalf("reverse scan: %v", err)
	}
	if rev[0] != model.EntityDeleted || rev[len(rev)-1] != model.EntityCreated {
		t.Errorf("reverse scan order wrong: %v", rev)
	}
}

// TestScanTimeIndexLegacyEntriesFallBack pins the migration story: an index
// entry written before tagging (empty value) is yielded with Tagged=false, and
// Resolve still reaches its event — the exact cost every entry paid before.
// Nothing is migrated in place; retention ages the old format out.
func TestScanTimeIndexLegacyEntriesFallBack(t *testing.T) {
	s := newTestStore(t)
	if err := s.Append(mkEntityEvent("h1", model.EntityUnchanged, ts(1))); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Strip the tag the write just laid down, recreating a pre-tagging row.
	var legacyKey []byte
	if err := s.ScanTimeIndex(context.Background(), ts(0), ts(10), false, func(e TimeIndexEntry) error {
		legacyKey = timeKey(ts(1).UnixNano(), e.Seq)
		return nil
	}); err != nil {
		t.Fatalf("locating entry: %v", err)
	}
	if err := s.db.Set(legacyKey, nil, pebble.Sync); err != nil {
		t.Fatalf("stripping tag: %v", err)
	}

	var entries []TimeIndexEntry
	if err := s.ScanTimeIndex(context.Background(), ts(0), ts(10), false, func(e TimeIndexEntry) error {
		entries = append(entries, e)
		return nil
	}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(entries) != 1 || entries[0].Tagged {
		t.Fatalf("legacy entry not yielded as untagged: %+v", entries)
	}
	ev, ok, err := s.Resolve(entries[0].Seq)
	if err != nil || !ok {
		t.Fatalf("resolving legacy entry: ok=%v err=%v", ok, err)
	}
	if ev.Entity == nil || ev.Entity.ChangeType != model.EntityUnchanged {
		t.Errorf("legacy entry resolved to the wrong event: %+v", ev)
	}
}

// TestScanTimeIndexAgreesWithMaintenance pins the interaction with
// maintenance, which deletes primary and index keys in one atomic batch: after
// coalescing, the scan yields exactly the surviving entries and every one of
// them resolves. A scan-based reader therefore never sees removed history —
// and if a crash window ever leaves an index key dangling, Resolve's ok=false
// answer (pinned above via the legacy path) treats it as absence, not error.
func TestScanTimeIndexAgreesWithMaintenance(t *testing.T) {
	s := newTestStore(t)
	if err := s.Append(
		mkEntityEvent("h1", model.EntityUnchanged, ts(1)),
		mkEntityEvent("h1", model.EntityUnchanged, ts(2)),
		mkEntityEvent("h1", model.EntityUnchanged, ts(3)),
		mkEntityEvent("h1", model.EntityAttributeUpdated, ts(4)),
	); err != nil {
		t.Fatalf("append: %v", err)
	}
	removed, err := s.CoalesceHeartbeats()
	if err != nil {
		t.Fatalf("coalesce: %v", err)
	}
	if removed == 0 {
		t.Fatal("coalescing removed nothing; the fixture no longer exercises a dangling index entry")
	}

	entries, resolved := 0, 0
	if err := s.ScanTimeIndex(context.Background(), ts(0), ts(10), false, func(e TimeIndexEntry) error {
		entries++
		if _, ok, rerr := s.Resolve(e.Seq); rerr != nil {
			return rerr
		} else if ok {
			resolved++
		}
		return nil
	}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if entries != 4-removed {
		t.Errorf("scan yielded %d entries after coalescing %d of 4; index keys must go with their records", entries, removed)
	}
	if resolved != entries {
		t.Errorf("%d of %d surviving entries failed to resolve", entries-resolved, entries)
	}
}
