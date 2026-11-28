package store

import (
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

func TestAppendedByChangeTypeFresh(t *testing.T) {
	s := newTestStore(t)
	got := s.AppendedByChangeType()
	wantKeys := []string{
		"entity.created",
		"entity.deleted",
		"entity.identity_changed",
		"entity.attribute_updated",
		"entity.state_changed",
		"entity.unchanged",
		"relation.added",
		"relation.removed",
		"relation.attribute_changed",
		"unknown",
	}
	if len(got) != len(wantKeys) {
		t.Fatalf("map has %d keys, want %d: %v", len(got), len(wantKeys), got)
	}
	for _, k := range wantKeys {
		if v, ok := got[k]; !ok {
			t.Errorf("missing key %q", k)
		} else if v != 0 {
			t.Errorf("key %q = %d, want 0", k, v)
		}
	}
}

func TestAppendedByChangeTypeCounts(t *testing.T) {
	s := newTestStore(t)
	ts := time.Unix(1700000000, 0)
	id1 := model.EntityID("e1")
	id2 := model.EntityID("e2")

	if err := s.Append(mkEntityEvent(id1, model.EntityCreated, ts)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Append(mkEntityEvent(id1, model.EntityUnchanged, ts), mkEntityEvent(id1, model.EntityUnchanged, ts)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Append(mkEntityEvent(id2, model.EntityCreated, ts), mkEntityEvent(id1, model.EntityStateChanged, ts)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Append(
		mkEntityEvent(id1, model.EntityUnchanged, ts),
		mkEntityEvent(id1, model.EntityUnchanged, ts),
		mkEntityEvent(id1, model.EntityDeleted, ts),
	); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Append(mkRelationEvent(id2, id1, ts)); err != nil {
		t.Fatalf("append: %v", err)
	}

	got := s.AppendedByChangeType()
	want := map[string]uint64{
		"entity.created":             2,
		"entity.deleted":             1,
		"entity.identity_changed":    0,
		"entity.attribute_updated":   0,
		"entity.state_changed":       1,
		"entity.unchanged":           4,
		"relation.added":             1,
		"relation.removed":           0,
		"relation.attribute_changed": 0,
		"unknown":                    0,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q = %d, want %d", k, got[k], v)
		}
	}

	var sum uint64
	for _, v := range got {
		sum += v
	}
	if seq := s.Sequence(); seq != sum {
		t.Errorf("Sequence() = %d, want sum of appended counts %d", seq, sum)
	}
	if sum != 9 {
		t.Errorf("sum of appended counts = %d, want 9", sum)
	}
}

func TestAppendedByChangeTypeFailedAppend(t *testing.T) {
	s := newTestStore(t)
	ts := time.Unix(1700000000, 0)
	id := model.EntityID("e1")

	err := s.Append(mkEntityEvent(id, model.EntityCreated, ts), mkEntityEvent(id, model.ChangeUnspecified, ts))
	if err == nil {
		t.Fatal("Append with invalid event succeeded, want error")
	}

	got := s.AppendedByChangeType()
	for k, v := range got {
		if v != 0 {
			t.Errorf("key %q = %d, want 0", k, v)
		}
	}
	if seq := s.Sequence(); seq != 0 {
		t.Errorf("Sequence() = %d, want 0", seq)
	}
}
