package logship

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/toise-dev/toise/internal/model"
)

func TestShipWritesCompressedSegment(t *testing.T) {
	ctx := context.Background()
	sink, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	events := make([]model.Event, 0, 50)
	for i := 0; i < 50; i++ {
		events = append(events, ev(fmt.Sprintf("e%02d", i)))
	}
	src := &fakeSource{events: events}
	sh := New(sink)
	if n, serr := sh.Ship(ctx, "t", src); serr != nil {
		t.Fatal(serr)
	} else if n != 50 {
		t.Fatalf("shipped %d events, want 50", n)
	}

	names, err := sink.List(ctx, "t/")
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, n := range names {
		if strings.HasSuffix(n, segZstExt) {
			found = append(found, n)
		}
	}
	if len(found) != 1 || len(names) != 1 {
		t.Fatalf("sink holds %v, want exactly one .seg.zst name", names)
	}

	stored, err := sink.Get(ctx, found[0])
	if err != nil {
		t.Fatal(err)
	}
	uncompressed, err := encodeSegment(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) >= len(uncompressed) {
		t.Fatalf("stored %d bytes, uncompressed %d bytes; compression did not shrink", len(stored), len(uncompressed))
	}

	var got []model.Event
	if err := sh.Replay(ctx, "t", func(ev model.Event) error {
		got = append(got, ev)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 50 {
		t.Fatalf("replayed %d events, want 50", len(got))
	}
	for i := range got {
		if got[i].Entity.Entity.ID != events[i].Entity.Entity.ID {
			t.Fatalf("event %d: got %q, want %q", i, got[i].Entity.Entity.ID, events[i].Entity.Entity.ID)
		}
	}
}

func TestTenantsSeesCompressedSegments(t *testing.T) {
	ctx := context.Background()
	sink, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sh := New(sink)
	if _, err = sh.Ship(ctx, "t", &fakeSource{events: []model.Event{ev("a")}}); err != nil {
		t.Fatal(err)
	}
	got, err := sh.Tenants(ctx)
	if err != nil || len(got) != 1 || got[0] != "t" {
		t.Fatalf("Tenants = (%v, %v), want [t]: a restore must see a log shipped only in the compressed format", got, err)
	}
}

func TestReplayMixedFormats(t *testing.T) {
	ctx := context.Background()
	sink, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// A legacy uncompressed segment covering (0,2].
	legacy := segmentLegacyName("t", 0, 2)
	legacyEvents := []model.Event{ev("a"), ev("b")}
	legacyData, err := encodeSegment(legacyEvents)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Put(ctx, legacy, legacyData); err != nil {
		t.Fatal(err)
	}

	// The shipper must resume at 2 by reading the legacy name and continue
	// with a compressed segment covering (2,5].
	src := &fakeSource{events: []model.Event{
		ev("a"), ev("b"), ev("c"), ev("d"), ev("e"),
	}}
	sh := New(sink)
	if n, err := sh.Ship(ctx, "t", src); err != nil {
		t.Fatal(err)
	} else if n != 3 {
		t.Fatalf("shipped %d events, want 3", n)
	}

	var got []model.Event
	if err := sh.Replay(ctx, "t", func(ev model.Event) error {
		got = append(got, ev)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c", "d", "e"}
	if len(got) != len(want) {
		t.Fatalf("replayed %d events, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].Entity.EventID != want[i] {
			t.Fatalf("event %d: got %q, want %q", i, got[i].Entity.EventID, want[i])
		}
	}
}

func TestReplayRejectsCorruptCompressedSegment(t *testing.T) {
	ctx := context.Background()
	sink, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	name := segmentName("t", 0, 1)
	garbage := []byte("this is not a zstd stream")
	if err = sink.Put(ctx, name, garbage); err != nil {
		t.Fatal(err)
	}

	sh := New(sink)
	err = sh.Replay(ctx, "t", func(model.Event) error { return nil })
	if err == nil {
		t.Fatal("Replay succeeded on a corrupt segment, want error")
	}
	if !strings.Contains(err.Error(), name) {
		t.Fatalf("error %q does not name the segment %q", err, name)
	}
}

// segmentLegacyName is the pre-compression "<tenant>/<from>-<to>.seg" name,
// still written by nothing but read everywhere.
func segmentLegacyName(tenant string, from, to uint64) string {
	return fmt.Sprintf("%s/%016x-%016x%s", tenant, from, to, segExt)
}
