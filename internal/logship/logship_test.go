package logship

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

var fixedTime = time.Unix(1_700_000_000, 0).UTC()

// fakeSource is an in-memory event log: events[i] has sequence i+1.
type fakeSource struct{ events []model.Event }

func (f *fakeSource) Sequence() uint64 { return uint64(len(f.events)) }

func (f *fakeSource) ScanFrom(after uint64, fn func(uint64, model.Event) error) error {
	for i := range f.events {
		seq := uint64(i + 1)
		if seq <= after {
			continue
		}
		if err := fn(seq, f.events[i]); err != nil {
			return err
		}
	}
	return nil
}

func ev(id string) model.Event {
	return model.Event{Entity: &model.EntityEvent{
		EventID:       id,
		ChangeType:    model.EntityCreated,
		Entity:        model.Entity{Type: model.TypeHost, Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue(id)}}},
		EventTime:     fixedTime,
		RecordedAt:    fixedTime,
		SchemaVersion: model.SchemaVersion,
	}}
}

func ids(events []model.Event) []string {
	out := make([]string, len(events))
	for i := range events {
		out[i] = events[i].Entity.EventID
	}
	return out
}

func TestShipReplayRoundTrip(t *testing.T) {
	ctx := context.Background()
	sink, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{events: []model.Event{ev("a"), ev("b"), ev("c")}}
	sh := New(sink)

	// First ship: all three events, one segment.
	n, err := sh.Ship(ctx, "acme", src)
	if err != nil || n != 3 {
		t.Fatalf("first ship = (%d, %v), want (3, nil)", n, err)
	}
	// Nothing new: a no-op.
	if n, err := sh.Ship(ctx, "acme", src); err != nil || n != 0 {
		t.Fatalf("idempotent ship = (%d, %v), want (0, nil)", n, err)
	}

	// Append two more; only the delta ships.
	src.events = append(src.events, ev("d"), ev("e"))
	if n, err := sh.Ship(ctx, "acme", src); err != nil || n != 2 {
		t.Fatalf("delta ship = (%d, %v), want (2, nil)", n, err)
	}

	// Replay reconstructs every event, in order, across both segments.
	var got []model.Event
	if err := sh.Replay(ctx, "acme", func(e model.Event) error { got = append(got, e); return nil }); err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c", "d", "e"}
	if g := ids(got); !equal(g, want) {
		t.Fatalf("replay order = %v, want %v", g, want)
	}
}

// TestCursorDerivedFromSink pins the crash-safety property: a fresh shipper with
// no in-memory cursor must resume from the sink and not re-ship.
func TestCursorDerivedFromSink(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sink, _ := NewFileSink(dir)
	src := &fakeSource{events: []model.Event{ev("a"), ev("b")}}
	if n, err := New(sink).Ship(ctx, "acme", src); err != nil || n != 2 {
		t.Fatalf("ship = (%d, %v), want (2, nil)", n, err)
	}

	// A brand-new shipper (simulating a process restart) over the same sink.
	sink2, _ := NewFileSink(dir)
	if n, err := New(sink2).Ship(ctx, "acme", src); err != nil || n != 0 {
		t.Fatalf("post-restart ship = (%d, %v), want (0, nil) — cursor must derive from the sink", n, err)
	}
}

// TestReplayDetectsGapAndOverlap pins the restore-integrity guard: a sink
// mutated after write (lifecycle expiry loses a middle object; a racing shipper
// writes an overlapping range) must fail the restore loudly instead of silently
// reconstructing a divergent log.
func TestReplayDetectsGapAndOverlap(t *testing.T) {
	ctx := context.Background()

	t.Run("gap from a lost middle segment", func(t *testing.T) {
		dir := t.TempDir()
		sink, _ := NewFileSink(dir)
		sh := New(sink)
		// three contiguous segments: (0,1], (1,2], (2,3]
		for i := 1; i <= 3; i++ {
			evs := []model.Event{ev("a"), ev("b"), ev("c")}[:i]
			if _, err := sh.Ship(ctx, "acme", &fakeSource{events: evs}); err != nil {
				t.Fatal(err)
			}
		}
		names, _ := sink.List(ctx, "acme/")
		if len(names) != 3 {
			t.Fatalf("want 3 segments, got %d", len(names))
		}
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(names[1]))); err != nil {
			t.Fatal(err)
		}
		err := sh.Replay(ctx, "acme", func(model.Event) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "gap") {
			t.Fatalf("Replay over a gap = %v, want a gap error", err)
		}
	})

	t.Run("overlapping segment", func(t *testing.T) {
		ctx := context.Background()
		sink, _ := NewFileSink(t.TempDir())
		sh := New(sink)
		if _, err := sh.Ship(ctx, "acme", &fakeSource{events: []model.Event{ev("a")}}); err != nil {
			t.Fatal(err)
		}
		if _, err := sh.Ship(ctx, "acme", &fakeSource{events: []model.Event{ev("a"), ev("b"), ev("c")}}); err != nil {
			t.Fatal(err)
		}
		// inject a segment (0,2] that overlaps the existing (1,3]
		seg, err := encodeSegment([]model.Event{ev("a"), ev("b")})
		if err != nil {
			t.Fatal(err)
		}
		if err = sink.Put(ctx, segmentName("acme", 0, 2), seg); err != nil {
			t.Fatal(err)
		}
		err = sh.Replay(ctx, "acme", func(model.Event) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "overlap") {
			t.Fatalf("Replay over an overlap = %v, want an overlap error", err)
		}
	})
}

func TestPerTenantIsolation(t *testing.T) {
	ctx := context.Background()
	sink, _ := NewFileSink(t.TempDir())
	sh := New(sink)
	if _, err := sh.Ship(ctx, "acme", &fakeSource{events: []model.Event{ev("a1")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.Ship(ctx, "globex", &fakeSource{events: []model.Event{ev("g1"), ev("g2")}}); err != nil {
		t.Fatal(err)
	}
	var acme []model.Event
	if err := sh.Replay(ctx, "acme", func(e model.Event) error { acme = append(acme, e); return nil }); err != nil {
		t.Fatal(err)
	}
	if g := ids(acme); !equal(g, []string{"a1"}) {
		t.Fatalf("acme replay = %v, want [a1] — a tenant must not see another's segments", g)
	}
}

func TestFileSinkPutGetAtomic(t *testing.T) {
	ctx := context.Background()
	sink, _ := NewFileSink(t.TempDir())
	if err := sink.Put(ctx, "t/x.seg", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := sink.Get(ctx, "t/x.seg")
	if err != nil || string(got) != "hello" {
		t.Fatalf("get = (%q, %v), want hello", got, err)
	}
	names, err := sink.List(ctx, "t/")
	if err != nil || len(names) != 1 || names[0] != "t/x.seg" {
		t.Fatalf("list = (%v, %v), want [t/x.seg]", names, err)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestNewS3SinkValidation pins the required-field checks; it builds the client
// (no network) and asserts the Sink interface is satisfied.
func TestNewS3SinkValidation(t *testing.T) {
	bad := []S3Config{
		{Bucket: "b", AccessKey: "a", SecretKey: "s"},   // no endpoint
		{Endpoint: "e", AccessKey: "a", SecretKey: "s"}, // no bucket
		{Endpoint: "e", Bucket: "b", SecretKey: "s"},    // no access key
		{Endpoint: "e", Bucket: "b", AccessKey: "a"},    // no secret key
	}
	for _, c := range bad {
		if _, err := NewS3Sink(c); err == nil {
			t.Errorf("expected error for incomplete config %+v", c)
		}
	}
	s, err := NewS3Sink(S3Config{Endpoint: "s3.example.com", Bucket: "b", AccessKey: "a", SecretKey: "s", UseSSL: true})
	if err != nil || s == nil {
		t.Fatalf("valid config: sink=%v err=%v", s, err)
	}
	var _ Sink = s
}
