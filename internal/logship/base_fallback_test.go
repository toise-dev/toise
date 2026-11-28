package logship

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

func mustPut(t *testing.T, sink Sink, name string, data []byte) {
	t.Helper()
	if err := sink.Put(context.Background(), name, data); err != nil {
		t.Fatalf("Put(%q): %v", name, err)
	}
}

func writeTestBase(t *testing.T, sink Sink, tenant string, seq uint64, horizon time.Time, ids []string) {
	t.Helper()
	data, err := encodeBase(baseHeader{Horizon: horizon, WrittenAt: horizon}, eventsOf(ids))
	if err != nil {
		t.Fatalf("encodeBase: %v", err)
	}
	mustPut(t, sink, baseName(tenant, seq), data)
}

func writeTestSegment(t *testing.T, sink Sink, tenant string, from, to uint64, ids []string) {
	t.Helper()
	data, err := encodeSegment(eventsOf(ids))
	if err != nil {
		t.Fatalf("encodeSegment: %v", err)
	}
	data, err = compressSegment(data)
	if err != nil {
		t.Fatalf("compressSegment: %v", err)
	}
	mustPut(t, sink, segmentName(tenant, from, to), data)
}

func idsEqual(got []string, want ...string) bool {
	return strings.Join(got, ",") == strings.Join(want, ",")
}

func TestReplayFallsBackToPreviousBase(t *testing.T) {
	tenant := "acme"
	sink, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	sh := New(sink)

	h1 := startClock
	h2 := startClock.Add(12 * time.Hour)

	writeTestBase(t, sink, tenant, 3, h1, mustIDs(3))
	writeTestBase(t, sink, tenant, 6, h2, mustIDs(6))
	writeTestSegment(t, sink, tenant, 3, 6, []string{"d", "e", "f"})
	writeTestSegment(t, sink, tenant, 6, 8, []string{"g", "h"})

	// Corrupt the newest base so restore must fall back to the older one.
	mustPut(t, sink, baseName(tenant, 6), []byte("this is not a base object"))

	var got []string
	horizon, err := sh.ReplayWithHorizon(context.Background(), tenant, func(e model.Event) error {
		got = append(got, e.Entity.EventID)
		return nil
	})
	if err != nil {
		t.Fatalf("ReplayWithHorizon: %v", err)
	}
	if !idsEqual(got, "a", "b", "c", "d", "e", "f", "g", "h") {
		t.Fatalf("applied ids = %v, want [a b c d e f g h]", got)
	}
	if !horizon.Equal(h1) {
		t.Fatalf("horizon = %v, want %v", horizon, h1)
	}
}

func TestReplayFallbackNeedsAnUnbrokenChain(t *testing.T) {
	tenant := "acme"
	sink, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	sh := New(sink)

	h1 := startClock
	h2 := startClock.Add(12 * time.Hour)

	writeTestBase(t, sink, tenant, 3, h1, mustIDs(3))
	writeTestBase(t, sink, tenant, 6, h2, mustIDs(6))
	// No segment (3,6]: the chain after base 3 is broken.
	writeTestSegment(t, sink, tenant, 6, 8, []string{"g", "h"})

	mustPut(t, sink, baseName(tenant, 6), []byte("this is not a base object"))

	var got []string
	err = sh.Replay(context.Background(), tenant, func(e model.Event) error {
		got = append(got, e.Entity.EventID)
		return nil
	})
	if err == nil {
		t.Fatal("Replay: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "gap") {
		t.Fatalf("error = %v, want a gap error", err)
	}
	if len(got) != 0 {
		t.Fatalf("apply was called %d times, want 0", len(got))
	}
}

func TestReplayNoUsableBaseReportsEveryError(t *testing.T) {
	tenant := "acme"
	sink, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	sh := New(sink)

	// Build a valid base, then flip the version byte so readBase rejects it.
	data, err := encodeBase(baseHeader{Horizon: startClock, WrittenAt: startClock}, eventsOf(mustIDs(3)))
	if err != nil {
		t.Fatalf("encodeBase: %v", err)
	}
	data[4] = 2
	mustPut(t, sink, baseName(tenant, 3), data)

	writeTestSegment(t, sink, tenant, 3, 5, []string{"d", "e"})

	var got []string
	err = sh.Replay(context.Background(), tenant, func(e model.Event) error {
		got = append(got, e.Entity.EventID)
		return nil
	})
	if err == nil {
		t.Fatal("Replay: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Fatalf("error = %v, want it to mention the underlying version error", err)
	}
	if len(got) != 0 {
		t.Fatalf("apply was called %d times, want 0", len(got))
	}
}

func TestReplayHealthyNewestBaseUnchanged(t *testing.T) {
	tenant := "acme"
	sink, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	sh := New(sink)

	h1 := startClock
	h2 := startClock.Add(12 * time.Hour)

	writeTestBase(t, sink, tenant, 3, h1, mustIDs(3))
	writeTestBase(t, sink, tenant, 6, h2, mustIDs(6))
	writeTestSegment(t, sink, tenant, 6, 8, []string{"g", "h"})

	var got []string
	horizon, err := sh.ReplayWithHorizon(context.Background(), tenant, func(e model.Event) error {
		got = append(got, e.Entity.EventID)
		return nil
	})
	if err != nil {
		t.Fatalf("ReplayWithHorizon: %v", err)
	}
	if !idsEqual(got, "a", "b", "c", "d", "e", "f", "g", "h") {
		t.Fatalf("applied ids = %v, want [a b c d e f g h]", got)
	}
	if !horizon.Equal(h2) {
		t.Fatalf("horizon = %v, want %v", horizon, h2)
	}
}
