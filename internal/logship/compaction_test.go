package logship

import (
	"context"
	"errors"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

var startClock = time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

// mustIDs returns the first n lowercase letter ids: "a", "b", ...
func mustIDs(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, string(rune('a'+i)))
	}
	return out
}

func eventsOf(ids []string) []model.Event {
	out := make([]model.Event, 0, len(ids))
	for _, id := range ids {
		out = append(out, ev(id))
	}
	return out
}

func replayIDs(t *testing.T, sh *Shipper, tenant string) []string {
	t.Helper()
	var got []string
	err := sh.Replay(context.Background(), tenant, func(e model.Event) error {
		got = append(got, e.Entity.EventID)
		return nil
	})
	if err != nil {
		t.Fatalf("Replay(%q): %v", tenant, err)
	}
	return got
}

func listAll(t *testing.T, sink Sink) []string {
	t.Helper()
	names, err := sink.List(context.Background(), "acme/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return names
}

func has(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func baseCount(names []string) int {
	n := 0
	for _, name := range names {
		if strings.HasPrefix(path.Base(name), "base-") {
			n++
		}
	}
	return n
}

// timeline drives the Compact scenario used by tests 3-6: at each step the
// clock advances to startClock+offset, the source grows by two events, Ship
// runs, then Compact runs with retention 24h and baseInterval 12h.
type timeline struct {
	t                   *testing.T
	dir                 Sink // the raw FileSink
	sh                  *Shipper
	now                 time.Time
	total               int
	retention, interval time.Duration
	src                 *fakeSource
}

func newTimeline(t *testing.T, wrap func(Sink) Sink) *timeline {
	t.Helper()
	dir, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	var sink Sink = dir
	if wrap != nil {
		sink = wrap(dir)
	}
	tl := &timeline{
		t:         t,
		dir:       dir,
		sh:        New(sink),
		now:       startClock,
		total:     0,
		retention: 24 * time.Hour,
		interval:  12 * time.Hour,
		src:       &fakeSource{},
	}
	tl.sh.now = func() time.Time { return tl.now }
	return tl
}

func (tl *timeline) step(offset time.Duration) error {
	tl.now = startClock.Add(offset)
	for i := 0; i < 2; i++ {
		tl.src.events = append(tl.src.events, ev(string(rune('a'+tl.total+i))))
	}
	tl.total += 2
	n, err := tl.sh.Ship(context.Background(), "acme", tl.src)
	if err != nil {
		tl.t.Fatalf("step %v: Ship: %v", offset, err)
	}
	if n != 2 {
		tl.t.Fatalf("step %v: Ship shipped %d events, want 2", offset, n)
	}
	return tl.sh.Compact(context.Background(), "acme", tl.src, tl.retention, tl.interval)
}

// failDeleteSink fails every Delete.
type failDeleteSink struct {
	Sink
}

func (f *failDeleteSink) Delete(ctx context.Context, name string) error {
	return errors.New("delete failed")
}

// corruptBaseSink returns garbage bytes for base objects while corrupt is set.
type corruptBaseSink struct {
	Sink
	corrupt bool
}

func (c *corruptBaseSink) Get(ctx context.Context, name string) ([]byte, error) {
	if c.corrupt && strings.HasPrefix(path.Base(name), "base-") {
		return []byte("garbage"), nil
	}
	return c.Sink.Get(ctx, name)
}

func TestCompactRetentionZeroIsNoop(t *testing.T) {
	ctx := context.Background()
	dir, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	sh := New(dir)
	sh.now = func() time.Time { return startClock }
	src := &fakeSource{events: eventsOf(mustIDs(2))}
	if n, err := sh.Ship(ctx, "acme", src); err != nil || n != 2 {
		t.Fatalf("Ship: n=%d err=%v", n, err)
	}
	if err := sh.Compact(ctx, "acme", src, 0, 12*time.Hour); err != nil {
		t.Fatalf("Compact with retention 0: %v", err)
	}
	names := listAll(t, dir)
	if got := baseCount(names); got != 0 {
		t.Fatalf("got %d base objects, want 0: %v", got, names)
	}
	if !has(names, segmentName("acme", 0, 2)) {
		t.Fatalf("segment (0,2] deleted: %v", names)
	}
}

func TestCompactWritesBaseWithHorizon(t *testing.T) {
	ctx := context.Background()
	dir, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	sh := New(dir)
	sh.now = func() time.Time { return startClock }
	hz := startClock.Add(-3 * time.Hour)
	src := &fakeSource{events: eventsOf(mustIDs(2)), horizon: hz}
	if n, shipErr := sh.Ship(ctx, "acme", src); shipErr != nil || n != 2 {
		t.Fatalf("Ship: n=%d err=%v", n, shipErr)
	}
	if compactErr := sh.Compact(ctx, "acme", src, 24*time.Hour, 12*time.Hour); compactErr != nil {
		t.Fatalf("Compact: %v", compactErr)
	}
	names := listAll(t, dir)
	if got := baseCount(names); got != 1 {
		t.Fatalf("got %d base objects, want 1: %v", got, names)
	}
	base := baseName("acme", 2)
	if !has(names, base) {
		t.Fatalf("base object %q missing: %v", base, names)
	}
	if !has(names, segmentName("acme", 0, 2)) {
		t.Fatalf("segment (0,2] deleted: %v", names)
	}
	data, err := dir.Get(ctx, base)
	if err != nil {
		t.Fatalf("Get base: %v", err)
	}
	h, events, err := decodeBase(data)
	if err != nil {
		t.Fatalf("decodeBase: %v", err)
	}
	if !h.Horizon.Equal(hz) {
		t.Fatalf("Horizon = %v, want %v", h.Horizon, hz)
	}
	if !h.WrittenAt.Equal(startClock) {
		t.Fatalf("WrittenAt = %v, want %v", h.WrittenAt, startClock)
	}
	if !equal(ids(events), mustIDs(2)) {
		t.Fatalf("base events = %v, want %v", ids(events), mustIDs(2))
	}
}

func TestCompactDeletesBehindOldEnoughBase(t *testing.T) {
	ctx := context.Background()
	tl := newTimeline(t, nil)

	twinDir, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	twinSh := New(twinDir)
	twinSrc := &fakeSource{}

	// check replays the compacted shipper and an uncompacted twin after each
	// step; both must yield exactly the events shipped so far, in order.
	check := func(want []string) {
		t.Helper()
		got := replayIDs(t, tl.sh, "acme")
		if !equal(got, want) {
			t.Fatalf("replay = %v, want %v", got, want)
		}
		twinSrc.events = eventsOf(want)
		if _, err := twinSh.Ship(ctx, "acme", twinSrc); err != nil {
			t.Fatalf("twin Ship: %v", err)
		}
		if got := replayIDs(t, twinSh, "acme"); !equal(got, want) {
			t.Fatalf("twin replay = %v, want %v", got, want)
		}
	}

	// Step 1: t0, events 1-2, base1 S=2.
	if err := tl.step(0); err != nil {
		t.Fatalf("step 1: %v", err)
	}
	names := listAll(t, tl.dir)
	if !has(names, baseName("acme", 2)) {
		t.Fatalf("base S=2 missing: %v", names)
	}
	if !has(names, segmentName("acme", 0, 2)) {
		t.Fatalf("segment (0,2] missing: %v", names)
	}
	check(mustIDs(2))

	// Step 2: t0+13h, events 3-4, base2 S=4. Nothing old enough to delete.
	if err := tl.step(13 * time.Hour); err != nil {
		t.Fatalf("step 2: %v", err)
	}
	names = listAll(t, tl.dir)
	if !has(names, baseName("acme", 4)) {
		t.Fatalf("base S=4 missing: %v", names)
	}
	for _, seg := range []string{segmentName("acme", 0, 2), segmentName("acme", 2, 4)} {
		if !has(names, seg) {
			t.Fatalf("segment %q deleted early: %v", seg, names)
		}
	}
	check(mustIDs(4))

	// Step 3: t0+26h, events 5-6, base3 S=6. Cut is base1 (26h old):
	// segment (0,2] is deleted (to=2 <= S=2); segments (2,4] and (4,6]
	// remain; bases S=2, 4, 6 all remain.
	if err := tl.step(26 * time.Hour); err != nil {
		t.Fatalf("step 3: %v", err)
	}
	names = listAll(t, tl.dir)
	for _, s := range []uint64{2, 4, 6} {
		if !has(names, baseName("acme", s)) {
			t.Fatalf("base S=%d missing: %v", s, names)
		}
	}
	if has(names, segmentName("acme", 0, 2)) {
		t.Fatalf("segment (0,2] not deleted: %v", names)
	}
	for from := uint64(2); from < 6; from += 2 {
		if !has(names, segmentName("acme", from, from+2)) {
			t.Fatalf("segment (%d,%d] missing: %v", from, from+2, names)
		}
	}
	check(mustIDs(6))

	// Step 4: t0+40h, events 7-8, base4 S=8. Cut is base2 (27h old):
	// segments (0,2] and (2,4] and base1 are deleted.
	if err := tl.step(40 * time.Hour); err != nil {
		t.Fatalf("step 4: %v", err)
	}
	names = listAll(t, tl.dir)
	if has(names, segmentName("acme", 0, 2)) || has(names, segmentName("acme", 2, 4)) {
		t.Fatalf("segments behind base S=4 not deleted: %v", names)
	}
	if has(names, baseName("acme", 2)) {
		t.Fatalf("base S=2 not deleted: %v", names)
	}
	for _, s := range []uint64{4, 6, 8} {
		if !has(names, baseName("acme", s)) {
			t.Fatalf("base S=%d missing: %v", s, names)
		}
	}
	for _, seg := range []string{segmentName("acme", 4, 6), segmentName("acme", 6, 8)} {
		if !has(names, seg) {
			t.Fatalf("segment %q deleted: %v", seg, names)
		}
	}
	check(mustIDs(8))
}

func TestCompactRestoreFromBaseMatchesRestoreFromZero(t *testing.T) {
	ctx := context.Background()
	tl := newTimeline(t, nil)
	for _, off := range []time.Duration{0, 13 * time.Hour, 26 * time.Hour, 40 * time.Hour} {
		if err := tl.step(off); err != nil {
			t.Fatalf("step %v: %v", off, err)
		}
	}
	twinDir, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	twinSh := New(twinDir)
	if n, err := twinSh.Ship(ctx, "acme", &fakeSource{events: eventsOf(mustIDs(8))}); err != nil || n != 8 {
		t.Fatalf("twin Ship: n=%d err=%v", n, err)
	}
	got := replayIDs(t, tl.sh, "acme")
	want := replayIDs(t, twinSh, "acme")
	if !equal(got, want) {
		t.Fatalf("compacted replay = %v, uncompacted replay = %v", got, want)
	}
	if len(got) != 8 {
		t.Fatalf("replayed %d events, want 8", len(got))
	}
}

func TestCrashAfterBaseBeforeDeletionLosesNothing(t *testing.T) {
	ctx := context.Background()
	tl := newTimeline(t, func(s Sink) Sink { return &failDeleteSink{Sink: s} })
	for _, off := range []time.Duration{0, 13 * time.Hour} {
		if err := tl.step(off); err != nil {
			t.Fatalf("step %v: %v", off, err)
		}
	}
	// Step 3 tries to delete behind base1 and fails.
	if err := tl.step(26 * time.Hour); err == nil {
		t.Fatal("Compact with failing Delete: want error, got nil")
	}
	names := listAll(t, tl.dir)
	if !has(names, baseName("acme", 6)) {
		t.Fatalf("new base S=6 missing: %v", names)
	}
	for from := uint64(0); from < 6; from += 2 {
		if !has(names, segmentName("acme", from, from+2)) {
			t.Fatalf("segment (%d,%d] missing: %v", from, from+2, names)
		}
	}
	if !has(names, baseName("acme", 2)) {
		t.Fatalf("base S=2 deleted despite failed Delete: %v", names)
	}
	if got := replayIDs(t, New(tl.dir), "acme"); !equal(got, mustIDs(6)) {
		t.Fatalf("replay after failed Compact = %v, want %v", got, mustIDs(6))
	}
	// Retry with a working sink over the same directory.
	now := startClock.Add(26 * time.Hour)
	sh := New(tl.dir)
	sh.now = func() time.Time { return now }
	if err := sh.Compact(ctx, "acme", &fakeSource{events: eventsOf(mustIDs(6))}, 24*time.Hour, 12*time.Hour); err != nil {
		t.Fatalf("retry Compact: %v", err)
	}
	if got := replayIDs(t, sh, "acme"); !equal(got, mustIDs(6)) {
		t.Fatalf("replay after retry = %v, want %v", got, mustIDs(6))
	}
}

func TestUnreadableBaseCausesNoDeletion(t *testing.T) {
	cs := &corruptBaseSink{}
	tl := newTimeline(t, func(s Sink) Sink { cs.Sink = s; return cs })
	for _, off := range []time.Duration{0, 13 * time.Hour} {
		if err := tl.step(off); err != nil {
			t.Fatalf("step %v: %v", off, err)
		}
	}
	before := listAll(t, tl.dir)
	cs.corrupt = true
	if err := tl.step(26 * time.Hour); err == nil {
		t.Fatal("Compact with unreadable base: want error, got nil")
	}
	after := listAll(t, tl.dir)
	for _, n := range before {
		if !has(after, n) {
			t.Fatalf("previously existing object %q disappeared: before=%v after=%v", n, before, after)
		}
	}
	if has(after, baseName("acme", 6)) {
		t.Fatalf("unreadable new base left in place, replay would start from it: %v", after)
	}
}

func TestReplayMixedLegacyZstAndBase(t *testing.T) {
	ctx := context.Background()
	dir, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	putLegacy := func(from, to uint64, events []model.Event) {
		t.Helper()
		data, err := encodeSegment(events)
		if err != nil {
			t.Fatalf("encodeSegment: %v", err)
		}
		name := strings.TrimSuffix(segmentName("acme", from, to), ".zst")
		if err := dir.Put(ctx, name, data); err != nil {
			t.Fatalf("Put %q: %v", name, err)
		}
	}
	putLegacy(0, 2, eventsOf(mustIDs(2)))

	sh := New(dir)
	sh.now = func() time.Time { return startClock }
	src := &fakeSource{events: eventsOf(mustIDs(4))}
	if n, err := sh.Ship(ctx, "acme", src); err != nil || n != 2 {
		t.Fatalf("Ship: n=%d err=%v", n, err)
	}
	if err := sh.Compact(ctx, "acme", src, 24*time.Hour, 12*time.Hour); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if n, err := sh.Ship(ctx, "acme", &fakeSource{events: eventsOf(mustIDs(6))}); err != nil || n != 2 {
		t.Fatalf("second Ship: n=%d err=%v", n, err)
	}
	putLegacy(6, 7, eventsOf([]string{"g"}))

	got := replayIDs(t, sh, "acme")
	if !equal(got, mustIDs(7)) {
		t.Fatalf("replay = %v, want %v", got, mustIDs(7))
	}
}

func TestReplayBaseChainErrors(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, setup func(dir Sink)) error {
		t.Helper()
		dir, err := NewFileSink(t.TempDir())
		if err != nil {
			t.Fatalf("NewFileSink: %v", err)
		}
		setup(dir)
		sh := New(dir)
		return sh.Replay(ctx, "acme", func(e model.Event) error { return nil })
	}
	putBase := func(dir Sink, data []byte) {
		t.Helper()
		if err := dir.Put(ctx, baseName("acme", 3), data); err != nil {
			t.Fatalf("Put base: %v", err)
		}
	}
	putSeg := func(dir Sink, from, to uint64) {
		t.Helper()
		data, err := encodeSegment(eventsOf(mustIDs(3)))
		if err != nil {
			t.Fatalf("encodeSegment: %v", err)
		}
		zst, err := compressSegment(data)
		if err != nil {
			t.Fatalf("compressSegment: %v", err)
		}
		if err := dir.Put(ctx, segmentName("acme", from, to), zst); err != nil {
			t.Fatalf("Put segment: %v", err)
		}
	}

	t.Run("overlap", func(t *testing.T) {
		err := run(t, func(dir Sink) {
			data, err := encodeBase(baseHeader{WrittenAt: startClock}, eventsOf(mustIDs(3)))
			if err != nil {
				t.Fatalf("encodeBase: %v", err)
			}
			putBase(dir, data)
			putSeg(dir, 2, 5)
		})
		if err == nil || !strings.Contains(err.Error(), "overlap") {
			t.Fatalf("want overlap error, got %v", err)
		}
	})

	t.Run("gap", func(t *testing.T) {
		err := run(t, func(dir Sink) {
			data, err := encodeBase(baseHeader{WrittenAt: startClock}, eventsOf(mustIDs(3)))
			if err != nil {
				t.Fatalf("encodeBase: %v", err)
			}
			putBase(dir, data)
			putSeg(dir, 4, 5)
		})
		if err == nil || !strings.Contains(err.Error(), "gap") {
			t.Fatalf("want gap error, got %v", err)
		}
	})

	t.Run("version", func(t *testing.T) {
		err := run(t, func(dir Sink) {
			data, err := encodeBase(baseHeader{WrittenAt: startClock}, eventsOf(mustIDs(3)))
			if err != nil {
				t.Fatalf("encodeBase: %v", err)
			}
			raw, err := decompressSegment(data)
			if err != nil {
				t.Fatalf("decompressSegment: %v", err)
			}
			raw[4] = 2
			bad, err := compressSegment(raw)
			if err != nil {
				t.Fatalf("compressSegment: %v", err)
			}
			putBase(dir, bad)
		})
		if err == nil || !strings.Contains(err.Error(), "version") {
			t.Fatalf("want version error, got %v", err)
		}
	})

	t.Run("badmagic", func(t *testing.T) {
		err := run(t, func(dir Sink) {
			data, err := encodeBase(baseHeader{WrittenAt: startClock}, eventsOf(mustIDs(3)))
			if err != nil {
				t.Fatalf("encodeBase: %v", err)
			}
			raw, err := decompressSegment(data)
			if err != nil {
				t.Fatalf("decompressSegment: %v", err)
			}
			raw[0] = 'X'
			bad, err := compressSegment(raw)
			if err != nil {
				t.Fatalf("compressSegment: %v", err)
			}
			putBase(dir, bad)
		})
		if err == nil {
			t.Fatal("want error for bad magic, got nil")
		}
	})
}

func TestTenantsSeesBaseOnlyTenant(t *testing.T) {
	ctx := context.Background()
	dir, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	data, err := encodeBase(baseHeader{WrittenAt: startClock}, eventsOf([]string{"x"}))
	if err != nil {
		t.Fatalf("encodeBase: %v", err)
	}
	if putErr := dir.Put(ctx, baseName("solo", 1), data); putErr != nil {
		t.Fatalf("Put base: %v", putErr)
	}
	tenants, err := New(dir).Tenants(ctx)
	if err != nil {
		t.Fatalf("Tenants: %v", err)
	}
	if !equal(tenants, []string{"solo"}) {
		t.Fatalf("Tenants = %v, want [solo]", tenants)
	}
}

func TestShipResumesFromBaseSequence(t *testing.T) {
	ctx := context.Background()
	dir, err := NewFileSink(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	data, err := encodeBase(baseHeader{WrittenAt: startClock}, eventsOf(mustIDs(3)))
	if err != nil {
		t.Fatalf("encodeBase: %v", err)
	}
	if putErr := dir.Put(ctx, baseName("acme", 3), data); putErr != nil {
		t.Fatalf("Put base: %v", putErr)
	}
	sh := New(dir)
	n, err := sh.Ship(ctx, "acme", &fakeSource{events: eventsOf(mustIDs(5))})
	if err != nil {
		t.Fatalf("Ship: %v", err)
	}
	if n != 2 {
		t.Fatalf("Ship shipped %d events, want 2", n)
	}
	names := listAll(t, dir)
	if !has(names, segmentName("acme", 3, 5)) {
		t.Fatalf("segment (3,5] missing: %v", names)
	}
	if got := len(names); got != 2 {
		t.Fatalf("got %d objects, want 2 (base + segment): %v", got, names)
	}
}

func TestBaseCodecRoundTrip(t *testing.T) {
	events := eventsOf(mustIDs(3))

	// Name helpers.
	if seq, ok := parseBaseName(baseName("acme", 7)); !ok || seq != 7 {
		t.Fatalf("parseBaseName(baseName) = %d, %v, want 7, true", seq, ok)
	}
	if _, ok := parseBaseName(segmentName("acme", 0, 7)); ok {
		t.Fatal("parseBaseName of an ordinary segment name returned ok")
	}

	// Round trip with a horizon.
	hz := startClock.Add(-48 * time.Hour)
	h := baseHeader{Horizon: hz, WrittenAt: startClock}
	data, err := encodeBase(h, events)
	if err != nil {
		t.Fatalf("encodeBase: %v", err)
	}
	h2, events2, err := decodeBase(data)
	if err != nil {
		t.Fatalf("decodeBase: %v", err)
	}
	if !h2.Horizon.Equal(hz) {
		t.Fatalf("Horizon = %v, want %v", h2.Horizon, hz)
	}
	if !h2.WrittenAt.Equal(startClock) {
		t.Fatalf("WrittenAt = %v, want %v", h2.WrittenAt, startClock)
	}
	if !equal(ids(events2), mustIDs(3)) {
		t.Fatalf("events = %v, want %v", ids(events2), mustIDs(3))
	}

	// Round trip with a zero horizon.
	data, err = encodeBase(baseHeader{WrittenAt: startClock}, events)
	if err != nil {
		t.Fatalf("encodeBase: %v", err)
	}
	h2, _, err = decodeBase(data)
	if err != nil {
		t.Fatalf("decodeBase: %v", err)
	}
	if !h2.Horizon.IsZero() {
		t.Fatalf("Horizon = %v, want zero time", h2.Horizon)
	}

	// Truncated payload: shorter than the 21-byte header once decompressed.
	raw, err := decompressSegment(data)
	if err != nil {
		t.Fatalf("decompressSegment: %v", err)
	}
	short, err := compressSegment(raw[:10])
	if err != nil {
		t.Fatalf("compressSegment: %v", err)
	}
	if _, _, decErr := decodeBase(short); decErr == nil {
		t.Fatal("decodeBase of truncated data: want error, got nil")
	}

	// Totally invalid bytes.
	bad, err := compressSegment([]byte(strings.Repeat("z", 30)))
	if err != nil {
		t.Fatalf("compressSegment: %v", err)
	}
	if _, _, err := decodeBase(bad); err == nil {
		t.Fatal("decodeBase of invalid bytes: want error, got nil")
	}
}
