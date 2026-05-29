package projection

import (
	"testing"

	"github.com/toise-dev/toise/internal/model"
)

// BenchmarkReplay measures rebuilding the projection from a log of entity
// creations. The phase-1 target is 1M events ≤ 30 s; this benchmark replays a
// fixed batch per iteration to derive per-event cost.
func BenchmarkReplay(b *testing.B) {
	const n = 50_000
	events := make([]model.Event, n)
	for i := 0; i < n; i++ {
		events[i] = entityCreated(model.NewEntityID(), model.TypeHost, kv("host.id", model.NewEventID()))
	}
	sc := evScanner{events: events}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g := New()
		if err := g.Replay(sc); err != nil {
			b.Fatal(err)
		}
	}
}
