package store

import (
	"context"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

// BenchmarkAppendBatch100 measures committing a batch of 100 events with Sync,
// the metric the phase-1 target (≤ 10 ms p99 on the reference profile) tracks.
func BenchmarkAppendBatch100(b *testing.B) {
	s, err := Open(b.TempDir(), DefaultConfig())
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()

	id := model.NewEntityID()
	batch := make([]model.Event, 100)
	base := time.Unix(1_700_000_000, 0).UTC()
	for i := range batch {
		batch[i] = mkEntityEvent(id, model.EntityStateChanged, base.Add(time.Duration(i)*time.Second))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.Append(batch...); err != nil {
			b.Fatalf("append: %v", err)
		}
	}
}

// BenchmarkScanTimeIndexSkipHeartbeats is the guardrail for #351/#352: a
// windowed change read over a heartbeat-dominated span must cost the index
// walk, never a resolution per skipped event. It loads one update drowned in
// heartbeats and scans the window excluding them — the production quiet-hour
// shape. If someone reintroduces per-event resolution on this path, this
// benchmark degrades by orders of magnitude and the CI gate catches it.
func BenchmarkScanTimeIndexSkipHeartbeats(b *testing.B) {
	dir := b.TempDir()
	s, err := Open(dir, DefaultConfig())
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()

	const heartbeats = 20_000
	evs := make([]model.Event, 0, heartbeats+1)
	for i := 0; i < heartbeats; i++ {
		evs = append(evs, mkEntityEvent("h1", model.EntityUnchanged, ts(int64(i))))
	}
	evs = append(evs, mkEntityEvent("h1", model.EntityAttributeUpdated, ts(heartbeats)))
	if err := s.Append(evs...); err != nil {
		b.Fatalf("append: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		kept := 0
		err := s.ScanTimeIndex(context.Background(), ts(0), ts(heartbeats+1), true, func(e TimeIndexEntry) error {
			if e.Tagged && e.ChangeType == model.EntityUnchanged {
				return nil
			}
			if _, ok, rerr := s.Resolve(e.Seq); rerr != nil {
				return rerr
			} else if ok {
				kept++
			}
			return nil
		})
		if err != nil || kept != 1 {
			b.Fatalf("scan: kept=%d err=%v", kept, err)
		}
	}
}
