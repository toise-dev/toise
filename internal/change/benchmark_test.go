package change

import (
	"fmt"
	"testing"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
)

// benchObservations builds n distinct host observations.
func benchObservations(n int) []EntityObservation {
	obs := make([]EntityObservation, n)
	for i := 0; i < n; i++ {
		obs[i] = EntityObservation{
			Type:       model.TypeHost,
			Identity:   []model.KeyValue{kv("host.id", fmt.Sprintf("srv-%05d", i))},
			Attributes: []model.KeyValue{kv("os.type", "linux")},
			EventTime:  t0,
		}
	}
	return obs
}

// benchEngine returns an engine backed by a real (Sync'ing) store, so the
// benchmarks capture the durable-append cost that batching amortizes.
func benchEngine(b *testing.B) *Engine {
	b.Helper()
	st, err := store.Open(b.TempDir(), store.DefaultConfig())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	return New(projection.New(), st, WithClock(fixedNow))
}

// BenchmarkIngestBatch100 ingests 100 entity observations as one batch — one
// durable Sync'd append for the whole batch (the OTLP receiver's path).
func BenchmarkIngestBatch100(b *testing.B) {
	e := benchEngine(b)
	obs := benchObservations(100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = e.Batch(func(bt *Batch) {
			for j := range obs {
				_, _ = bt.ObserveEntity(obs[j])
			}
		})
	}
}

// BenchmarkIngestPerEvent100 ingests the same 100 observations one at a time —
// one Sync per event (the pre-batch path) — to quantify what batching saves.
func BenchmarkIngestPerEvent100(b *testing.B) {
	e := benchEngine(b)
	obs := benchObservations(100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range obs {
			_, _ = e.ObserveEntity(obs[j])
		}
	}
}
