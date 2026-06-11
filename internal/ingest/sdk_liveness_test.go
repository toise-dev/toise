package ingest

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/pkg/emit"
)

// fakeClock is a mutable clock shared between the test and the engine: the
// receiver's gRPC handlers read it from server goroutines, so it is locked.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// startIngestWithClock is startIngest with the engine's clock injected and the
// engine + projection returned, for tests that drive the liveness sweep.
func startIngestWithClock(t *testing.T, clk *fakeClock) (string, *change.Engine, *projection.Graph) {
	t.Helper()
	st, err := store.Open(t.TempDir(), store.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	g := projection.New()
	eng := change.New(g, st, change.WithRelationBuffer(30*time.Second), change.WithClock(clk.Now))
	rec := NewReceiver(eng, nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = rec.Serve(lis) }()
	t.Cleanup(rec.Stop)
	return lis.Addr().String(), eng, g
}

func emitClient(t *testing.T, addr, instanceID string) *emit.Client {
	t.Helper()
	client, err := emit.New(emit.Options{Endpoint: addr, ServiceName: "liveness-producer", ServiceInstanceID: instanceID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestSDKIntervalArmsLiveness closes the first #160 coverage gap: nothing tied
// the entity.report.interval spelling end to end — the constant could drift on
// either side and every test stayed green while the liveness backstop silently
// disarmed for all producers. Here an SDK-built export carries an interval
// through the real receiver, and the engine must have ARMED liveness with it:
// the sweep leaves the entity alone inside the interval and reaps it once the
// producer goes silent past it.
func TestSDKIntervalArmsLiveness(t *testing.T) {
	clk := &fakeClock{now: time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)}
	addr, eng, g := startIngestWithClock(t, clk)
	client := emitClient(t, addr, "interval-01")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.State(ctx, emit.Entity{
		Type:     "host",
		ID:       map[string]string{"host.id": "live-1"},
		Interval: time.Minute,
	}); err != nil {
		t.Fatalf("SDK State: %v", err)
	}
	if g.EntityCount() != 1 {
		t.Fatalf("after emit: %d entities, want 1", g.EntityCount())
	}

	clk.Advance(30 * time.Second)
	if n := eng.Sweep(); n != 0 {
		t.Fatalf("sweep inside the interval expired %d, want 0", n)
	}
	if g.EntityCount() != 1 {
		t.Fatal("entity expired inside its declared interval")
	}

	clk.Advance(time.Minute) // 90s since the emit: past the 60s interval
	if n := eng.Sweep(); n != 1 {
		t.Fatalf("sweep past the interval expired %d, want 1 — the SDK interval did not arm the engine's liveness backstop", n)
	}
	if g.EntityCount() != 0 {
		t.Fatal("stale entity survived the sweep")
	}
}

// TestPerProducerReferenceCounting closes the second #160 coverage gap: no
// test set service.instance.id as a resource attribute and asserted ADR 0019
// reference counting through the real receiver. Two SDK producers observe the
// SAME entity; producer A going silent past its interval (removal by absence)
// must release only A's reference — the entity stays while B references it,
// and goes only when B releases too.
func TestPerProducerReferenceCounting(t *testing.T) {
	clk := &fakeClock{now: time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)}
	addr, eng, g := startIngestWithClock(t, clk)
	producerA := emitClient(t, addr, "ref-producer-a")
	producerB := emitClient(t, addr, "ref-producer-b")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	shared := emit.Entity{Type: "host", ID: map[string]string{"host.id": "shared-1"}}
	withInterval := shared
	withInterval.Interval = time.Minute
	if err := producerA.State(ctx, withInterval); err != nil {
		t.Fatalf("producer A State: %v", err)
	}
	if err := producerB.State(ctx, shared); err != nil { // explicit-only reference
		t.Fatalf("producer B State: %v", err)
	}
	if g.EntityCount() != 1 {
		t.Fatalf("two producers observing one entity: %d entities, want 1", g.EntityCount())
	}

	// A goes silent past its interval; B still references the entity.
	clk.Advance(5 * time.Minute)
	if n := eng.Sweep(); n != 0 {
		t.Fatalf("sweep expired %d, want 0 — producer A's lapse must only release A's reference", n)
	}
	if g.EntityCount() != 1 {
		t.Fatal("entity deleted while producer B still references it — per-producer reference counting collapsed (ADR 0019)")
	}

	// A's explicit release is the drift-sensitive probe: if either side spelled
	// the producer resource attribute differently, both producers collapse into
	// one anonymous reference and this delete takes the entity down with it.
	if err := producerA.Delete(ctx, shared); err != nil {
		t.Fatalf("producer A Delete: %v", err)
	}
	if g.EntityCount() != 1 {
		t.Fatal("producer A's release deleted the entity out from under producer B (ADR 0019)")
	}

	// B releases the last reference: now the entity goes.
	if err := producerB.Delete(ctx, shared); err != nil {
		t.Fatalf("producer B Delete: %v", err)
	}
	if g.EntityCount() != 0 {
		t.Fatal("entity survived the last producer's release")
	}
}
