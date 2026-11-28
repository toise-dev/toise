package logship

import (
	"bytes"
	"context"
	"path"
	"strings"
	"testing"
	"time"
)

// countingSink wraps a Sink and records how reads are served: full Get calls,
// ranged GetPrefix calls, the names fetched in full, and the longest GetPrefix
// response.
type countingSink struct {
	Sink
	gets        int
	getNames    []string
	prefixes    int
	prefixMax   int
	prefixTotal int
}

func (c *countingSink) Get(ctx context.Context, name string) ([]byte, error) {
	c.gets++
	c.getNames = append(c.getNames, name)
	return c.Sink.Get(ctx, name)
}

func (c *countingSink) GetPrefix(ctx context.Context, name string, n int) ([]byte, error) {
	c.prefixes++
	data, err := c.Sink.GetPrefix(ctx, name, n)
	if err != nil {
		return nil, err
	}
	if len(data) > c.prefixMax {
		c.prefixMax = len(data)
	}
	c.prefixTotal += len(data)
	return data, nil
}

func TestCompactCutReadsHeadersOnly(t *testing.T) {
	ctx := context.Background()
	tl := newTimeline(t, nil)
	tl.retention = 7 * 24 * time.Hour
	tl.interval = 24 * time.Hour
	for _, off := range []time.Duration{0, 24 * time.Hour, 48 * time.Hour} {
		if err := tl.step(off); err != nil {
			t.Fatalf("step %v: %v", off, err)
		}
	}
	// Three bases now exist, written at T, T+1d and T+2d.

	tl.now = startClock.Add(8 * 24 * time.Hour)
	for i := 0; i < 2; i++ {
		tl.src.events = append(tl.src.events, ev(string(rune('a'+tl.total+i))))
	}
	tl.total += 2
	if n, err := tl.sh.Ship(ctx, "acme", tl.src); err != nil || n != 2 {
		t.Fatalf("Ship: n=%d err=%v", n, err)
	}

	cs := &countingSink{Sink: tl.dir}
	sh := New(cs)
	sh.now = func() time.Time { return tl.now }
	if err := sh.Compact(ctx, "acme", tl.src, 7*24*time.Hour, 24*time.Hour); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	baseGets := 0
	for _, name := range cs.getNames {
		if strings.HasPrefix(path.Base(name), "base-") {
			baseGets++
		}
	}
	if baseGets != 1 {
		t.Fatalf("full Get calls on base objects = %d, want 1 (the read-back): %v", baseGets, cs.getNames)
	}
	if cs.prefixes == 0 {
		t.Fatalf("no GetPrefix calls; headers were not read via ranged reads")
	}
	if cs.prefixMax > baseHeaderLen {
		t.Fatalf("largest GetPrefix response = %d bytes, want <= %d", cs.prefixMax, baseHeaderLen)
	}
}

func TestBaseHeaderIsReadableWithoutDecompression(t *testing.T) {
	hz := startClock.Add(-3 * time.Hour)
	at := startClock.Add(5 * time.Minute)
	data, err := encodeBase(baseHeader{Horizon: hz, WrittenAt: at}, eventsOf(mustIDs(4)))
	if err != nil {
		t.Fatalf("encodeBase: %v", err)
	}
	if len(data) < baseHeaderLen {
		t.Fatalf("encoded base is %d bytes, shorter than the fixed header", len(data))
	}
	h, err := parseBaseHeader(data[:baseHeaderLen])
	if err != nil {
		t.Fatalf("parseBaseHeader: %v", err)
	}
	if !h.Horizon.Equal(hz) {
		t.Fatalf("Horizon = %v, want %v", h.Horizon, hz)
	}
	if !h.WrittenAt.Equal(at) {
		t.Fatalf("WrittenAt = %v, want %v", h.WrittenAt, at)
	}
}

func TestFileSinkGetPrefix(t *testing.T) {
	ctx := context.Background()
	sink, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	short := []byte("tiny")
	if err = sink.Put(ctx, "acme/short", short); err != nil {
		t.Fatalf("Put short: %v", err)
	}
	got, err := sink.GetPrefix(ctx, "acme/short", 32)
	if err != nil {
		t.Fatalf("GetPrefix short: %v", err)
	}
	if !bytes.Equal(got, short) {
		t.Fatalf("GetPrefix short = %q, want %q", got, short)
	}

	long := make([]byte, 1024)
	for i := range long {
		long[i] = byte(i)
	}
	if err = sink.Put(ctx, "acme/long", long); err != nil {
		t.Fatalf("Put long: %v", err)
	}
	got, err = sink.GetPrefix(ctx, "acme/long", 16)
	if err != nil {
		t.Fatalf("GetPrefix long: %v", err)
	}
	if len(got) != 16 {
		t.Fatalf("GetPrefix long returned %d bytes, want 16", len(got))
	}
	if !bytes.Equal(got, long[:16]) {
		t.Fatalf("GetPrefix long = %x, want %x", got, long[:16])
	}
}
