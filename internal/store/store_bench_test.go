package store

import (
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
