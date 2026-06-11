package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), DefaultConfig())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkEntityEvent(id model.EntityID, ct model.ChangeType, ts time.Time) model.Event {
	ee := model.EntityEvent{
		EventID:    model.NewEventID(),
		ChangeType: ct,
		Entity: model.Entity{
			ID:       id,
			Type:     model.TypeHost,
			Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue(string(id))}},
		},
		EventTime:     ts,
		RecordedAt:    ts,
		SchemaVersion: model.SchemaVersion,
	}
	return model.Event{Entity: &ee}
}

func mkRelationEvent(from, to model.EntityID, ts time.Time) model.Event {
	re := model.RelationEvent{
		EventID:       model.NewEventID(),
		ChangeType:    model.RelationAdded,
		Relation:      model.NewRelation(model.RelRunsOn, from, to),
		EventTime:     ts,
		RecordedAt:    ts,
		SchemaVersion: model.SchemaVersion,
	}
	return model.Event{Relation: &re}
}

func ts(sec int64) time.Time { return time.Unix(1_700_000_000+sec, 0).UTC() }

func scanAll(t *testing.T, s *Store) []model.Event {
	t.Helper()
	var out []model.Event
	var last uint64
	if err := s.Scan(func(seq uint64, ev model.Event) error {
		if seq <= last {
			t.Errorf("scan not ordered: seq %d after %d", seq, last)
		}
		last = seq
		out = append(out, ev)
		return nil
	}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

func TestAppendAndScan(t *testing.T) {
	s := newTestStore(t)
	a, b := model.NewEntityID(), model.NewEntityID()
	events := []model.Event{
		mkEntityEvent(a, model.EntityCreated, ts(0)),
		mkEntityEvent(b, model.EntityCreated, ts(1)),
		mkRelationEvent(a, b, ts(2)),
	}
	if err := s.Append(events...); err != nil {
		t.Fatalf("append: %v", err)
	}
	if got := s.Sequence(); got != 3 {
		t.Errorf("sequence = %d, want 3", got)
	}
	got := scanAll(t, s)
	if len(got) != 3 {
		t.Fatalf("scanned %d events, want 3", len(got))
	}
	if got[0].Entity == nil || got[0].Entity.Entity.ID != a {
		t.Error("first event mismatch")
	}
	if got[2].Relation == nil || got[2].Relation.Relation.From != a {
		t.Error("relation event mismatch")
	}
}

func TestReadByEntity(t *testing.T) {
	s := newTestStore(t)
	a, b := model.NewEntityID(), model.NewEntityID()
	if err := s.Append(
		mkEntityEvent(a, model.EntityCreated, ts(0)),
		mkEntityEvent(a, model.EntityStateChanged, ts(1)),
		mkEntityEvent(b, model.EntityCreated, ts(2)),
		mkRelationEvent(a, b, ts(3)),
	); err != nil {
		t.Fatalf("append: %v", err)
	}
	// a: two entity events + the relation (indexed under both endpoints) = 3.
	got, err := s.ReadByEntity(context.Background(), a)
	if err != nil {
		t.Fatalf("read by entity: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("entity a: got %d events, want 3", len(got))
	}
	// b: one entity event + the relation = 2.
	gotB, _ := s.ReadByEntity(context.Background(), b)
	if len(gotB) != 2 {
		t.Errorf("entity b: got %d events, want 2", len(gotB))
	}
}

func TestReadByType(t *testing.T) {
	s := newTestStore(t)
	a := model.NewEntityID()
	if err := s.Append(
		mkEntityEvent(a, model.EntityCreated, ts(0)),
		mkEntityEvent(a, model.EntityStateChanged, ts(1)),
		mkEntityEvent(a, model.EntityStateChanged, ts(2)),
	); err != nil {
		t.Fatalf("append: %v", err)
	}
	created, _ := s.ReadByType(context.Background(), model.EntityCreated)
	if len(created) != 1 {
		t.Errorf("created: got %d, want 1", len(created))
	}
	state, _ := s.ReadByType(context.Background(), model.EntityStateChanged)
	if len(state) != 2 {
		t.Errorf("state_changed: got %d, want 2", len(state))
	}
}

func TestReadByTimeRange(t *testing.T) {
	s := newTestStore(t)
	a := model.NewEntityID()
	for i := int64(0); i < 5; i++ {
		if err := s.Append(mkEntityEvent(a, model.EntityStateChanged, ts(i))); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	// [ts(1), ts(4)) -> ts(1), ts(2), ts(3) = 3 events.
	got, err := s.ReadByTimeRange(context.Background(), ts(1), ts(4))
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("range: got %d events, want 3", len(got))
	}
}

func TestSequenceRecoveryAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	a := model.NewEntityID()
	if err = s.Append(mkEntityEvent(a, model.EntityCreated, ts(0)), mkEntityEvent(a, model.EntityStateChanged, ts(1))); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	if got := s2.Sequence(); got != 2 {
		t.Errorf("recovered sequence = %d, want 2", got)
	}
	if len(scanAll(t, s2)) != 2 {
		t.Error("events not recovered after reopen")
	}
	// New appends continue the sequence.
	if err := s2.Append(mkEntityEvent(a, model.EntityDeleted, ts(2))); err != nil {
		t.Fatalf("append after reopen: %v", err)
	}
	if got := s2.Sequence(); got != 3 {
		t.Errorf("sequence after reopen-append = %d, want 3", got)
	}
}

func TestCoalesceHeartbeats(t *testing.T) {
	s := newTestStore(t)
	a := model.NewEntityID()
	batch := []model.Event{mkEntityEvent(a, model.EntityCreated, ts(0))}
	for i := int64(1); i <= 10; i++ { // 10 consecutive heartbeats
		batch = append(batch, mkEntityEvent(a, model.EntityUnchanged, ts(i)))
	}
	batch = append(batch, mkEntityEvent(a, model.EntityStateChanged, ts(11)))
	if err := s.Append(batch...); err != nil {
		t.Fatalf("append: %v", err)
	}

	removed, err := s.CoalesceHeartbeats()
	if err != nil {
		t.Fatalf("coalesce: %v", err)
	}
	// 10 heartbeats -> keep first + last, drop 8.
	if removed != 8 {
		t.Errorf("removed %d, want 8", removed)
	}
	got := scanAll(t, s)
	// created + 2 kept heartbeats + state_changed = 4.
	if len(got) != 4 {
		t.Fatalf("after coalesce: %d events, want 4", len(got))
	}
	// Meaningful events survive.
	hb, _ := s.ReadByType(context.Background(), model.EntityUnchanged)
	if len(hb) != 2 {
		t.Errorf("kept %d heartbeats, want 2", len(hb))
	}
	if c, _ := s.ReadByType(context.Background(), model.EntityCreated); len(c) != 1 {
		t.Error("created event lost")
	}
	if sc, _ := s.ReadByType(context.Background(), model.EntityStateChanged); len(sc) != 1 {
		t.Error("state_changed event lost")
	}

	// Idempotent: a second pass removes nothing.
	if again, _ := s.CoalesceHeartbeats(); again != 0 {
		t.Errorf("second coalesce removed %d, want 0", again)
	}
}

func TestAppendValidationRejectsBadEvent(t *testing.T) {
	s := newTestStore(t)
	bad := model.Event{Entity: &model.EntityEvent{ChangeType: model.EntityCreated}} // no entity identity, zero times
	if err := s.Append(bad); err == nil {
		t.Fatal("expected validation error")
	}
	if s.Sequence() != 0 {
		t.Errorf("sequence advanced on rejected append: %d", s.Sequence())
	}
}

func TestPrefixUpperBound(t *testing.T) {
	if got := prefixUpperBound([]byte("evt/")); string(got) != "evt0" {
		t.Errorf("prefixUpperBound = %q", got)
	}
	if prefixUpperBound([]byte{0xff, 0xff}) != nil {
		t.Error("all-0xff prefix should have no upper bound")
	}
}

// TestReadsHonorContextCancellation pins the read-path contract: a canceled
// context stops a scan instead of reading the whole range (#115).
func TestReadsHonorContextCancellation(t *testing.T) {
	s, err := Open(t.TempDir(), DefaultConfig())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	when := time.Unix(1_700_000_000, 0).UTC()
	ev := model.Event{Entity: &model.EntityEvent{
		EventID: model.NewEventID(), ChangeType: model.EntityCreated,
		Entity: model.Entity{ID: "e1", Type: model.TypeHost,
			Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue("h1")}}},
		EventTime: when, RecordedAt: when, SchemaVersion: model.SchemaVersion,
	}}
	if err := s.Append(ev); err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ReadByTimeRange(canceled, when.Add(-time.Hour), when.Add(time.Hour)); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadByTimeRange with canceled ctx = %v, want context.Canceled", err)
	}
	if _, err := s.ReadByEntity(canceled, "e1"); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadByEntity with canceled ctx = %v, want context.Canceled", err)
	}
	if _, err := s.ReadByTimeRange(context.Background(), when.Add(-time.Hour), when.Add(time.Hour)); err != nil {
		t.Errorf("live ctx read: %v", err)
	}
}

// TestPruneHorizonPersisted pins #135: the latest prune cutoff survives a
// reopen, so as-of reads can refuse instants whose events are gone.
func TestPruneHorizonPersisted(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !s.PruneHorizon().IsZero() {
		t.Error("fresh store must have a zero horizon")
	}
	when := time.Unix(1_700_000_000, 0).UTC()
	mk := func(id string, ct model.ChangeType, at time.Time) model.Event {
		return model.Event{Entity: &model.EntityEvent{
			EventID: model.NewEventID(), ChangeType: ct,
			Entity: model.Entity{ID: model.EntityID(id), Type: model.TypeHost,
				Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue(id)}}},
			EventTime: at, RecordedAt: at, SchemaVersion: model.SchemaVersion,
		}}
	}
	for _, ev := range []model.Event{
		mk("a", model.EntityCreated, when),
		mk("a", model.EntityAttributeUpdated, when.Add(time.Hour)),
	} {
		if aerr := s.Append(ev); aerr != nil {
			t.Fatal(aerr)
		}
	}
	cutoff := when.Add(30 * time.Minute)
	if _, _, perr := s.PruneOlderThan(cutoff); perr != nil {
		t.Fatal(perr)
	}
	if got := s.PruneHorizon(); !got.Equal(cutoff) {
		t.Fatalf("horizon = %v, want %v", got, cutoff)
	}
	if cerr := s.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	reopened, err := Open(dir, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got := reopened.PruneHorizon(); !got.Equal(cutoff) {
		t.Fatalf("horizon after reopen = %v, want %v", got, cutoff)
	}
}
