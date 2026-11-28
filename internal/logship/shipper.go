package logship

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/proto"

	"github.com/toise-dev/toise/internal/model"
	toisev1 "github.com/toise-dev/toise/proto/toise/v1"
)

// EventSource is the slice of the event log the shipper needs: the current max
// sequence, a tail scan in sequence order, and the local prune horizon so a
// base segment can carry it. *store.Store satisfies it.
type EventSource interface {
	Sequence() uint64
	ScanFrom(afterSeq uint64, fn func(seq uint64, ev model.Event) error) error
	PruneHorizon() time.Time
}

const (
	segExt    = ".seg"     // legacy, uncompressed segments
	segZstExt = ".seg.zst" // compressed segments written by Ship
)

// One package-level zstd encoder and decoder, created lazily and safe for
// concurrent use: EncodeAll/DecodeAll are stateless per call. The decoder caps
// its memory budget so a corrupt or hostile object cannot make it allocate
// without bound.
var (
	zstdEncOnce sync.Once
	zstdEnc     *zstd.Encoder
	zstdEncErr  error

	zstdDecOnce sync.Once
	zstdDec     *zstd.Decoder
	zstdDecErr  error
)

func zstdEncoder() (*zstd.Encoder, error) {
	zstdEncOnce.Do(func() {
		zstdEnc, zstdEncErr = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	})
	if zstdEncErr != nil {
		return nil, zstdEncErr
	}
	return zstdEnc, nil
}

func zstdDecoder() (*zstd.Decoder, error) {
	zstdDecOnce.Do(func() {
		zstdDec, zstdDecErr = zstd.NewReader(nil, zstd.WithDecoderMaxMemory(1<<30))
	})
	if zstdDecErr != nil {
		return nil, zstdDecErr
	}
	return zstdDec, nil
}

// compressSegment compresses the framed segment bytes as a single zstd stream.
func compressSegment(data []byte) ([]byte, error) {
	enc, err := zstdEncoder()
	if err != nil {
		return nil, fmt.Errorf("logship: zstd encoder: %w", err)
	}
	return enc.EncodeAll(data, nil), nil
}

// decompressSegment reverses compressSegment.
func decompressSegment(data []byte) ([]byte, error) {
	dec, err := zstdDecoder()
	if err != nil {
		return nil, fmt.Errorf("logship: zstd decoder: %w", err)
	}
	return dec.DecodeAll(data, nil)
}

// Shipper exports a tenant's event log to a Sink as immutable, contiguous
// segments. The cursor (last shipped sequence) is derived from the sink, so the
// shipper holds no durable state and a crash never duplicates or skips a
// segment: the next Ship re-derives where to resume.
type Shipper struct {
	sink Sink

	mu         sync.Mutex
	cursor     map[string]uint64    // tenant -> last shipped seq (cache of the sink-derived value)
	now        func() time.Time     // clock, injectable for tests
	lastBaseAt map[string]time.Time // tenant -> write time of the newest known base
}

// New returns a Shipper writing to sink.
func New(sink Sink) *Shipper {
	return &Shipper{
		sink:       sink,
		cursor:     map[string]uint64{},
		now:        time.Now,
		lastBaseAt: map[string]time.Time{},
	}
}

// Ship exports the events (lastShipped, current] for one tenant as a single
// compressed segment object and returns how many it shipped (0 when already
// current). The range is closed at the sequence read before the scan, so events
// appended during the scan ship on the next call.
func (sh *Shipper) Ship(ctx context.Context, tenant string, src EventSource) (int, error) {
	from, err := sh.lastShipped(ctx, tenant)
	if err != nil {
		return 0, err
	}
	current := src.Sequence()
	if current <= from {
		return 0, nil
	}

	events := make([]model.Event, 0, current-from)
	errStop := errors.New("stop")
	scanErr := src.ScanFrom(from, func(seq uint64, ev model.Event) error {
		if seq > current {
			return errStop
		}
		events = append(events, ev)
		return nil
	})
	if scanErr != nil && !errors.Is(scanErr, errStop) {
		return 0, fmt.Errorf("logship: scanning %s from %d: %w", tenant, from, scanErr)
	}
	if len(events) == 0 {
		return 0, nil
	}

	framed, err := encodeSegment(events)
	if err != nil {
		return 0, err
	}
	seg, err := compressSegment(framed)
	if err != nil {
		return 0, fmt.Errorf("logship: compressing segment %s: %w", segmentName(tenant, from, current), err)
	}
	name := segmentName(tenant, from, current)
	if err := sh.sink.Put(ctx, name, seg); err != nil {
		// The Put may have landed despite the error (ambiguous sink failure).
		// Drop the cached cursor so the next Ship re-derives it from the sink,
		// picking up a possibly-landed segment instead of overlapping it.
		sh.mu.Lock()
		delete(sh.cursor, tenant)
		sh.mu.Unlock()
		return 0, err
	}
	sh.mu.Lock()
	sh.cursor[tenant] = current
	sh.mu.Unlock()
	return len(events), nil
}

// Replay reads every segment for a tenant in sequence order and calls apply for
// each event — the building block of restore (the caller persists them, e.g.
// via store.Append into a fresh data dir). Ship writes contiguous (from, to]
// ranges, but the sink can be mutated out from under us (lifecycle expiry, a
// lost object, a partial mirror, two shippers racing one sink), so Replay
// validates the chain — first segment from 0, each segment resuming exactly
// where the previous ended — and hard-errors on a gap or overlap rather than
// silently reconstructing a divergent log. Restore starts from the newest base
// when one exists. Both the legacy uncompressed (".seg") and compressed
// (".seg.zst") formats are accepted, so a log shipped before compression was
// introduced and continued after it replays as one chain.
func (sh *Shipper) Replay(ctx context.Context, tenant string, apply func(model.Event) error) error {
	_, err := sh.replay(ctx, tenant, apply)
	return err
}

// ReplayWithHorizon is Replay that also reports the prune horizon recorded in
// the base it started from (the zero time when it replayed without a base), so
// a restore can refuse as-of reads older than the history it holds.
func (sh *Shipper) ReplayWithHorizon(ctx context.Context, tenant string, apply func(model.Event) error) (time.Time, error) {
	return sh.replay(ctx, tenant, apply)
}

type segInfo struct {
	name string
	from uint64
	to   uint64
}

// sortedSegs parses segment object names and returns them sorted by their
// starting sequence.
func sortedSegs(tenant string, names []string) ([]segInfo, error) {
	segs := make([]segInfo, 0, len(names))
	for _, name := range names {
		from, to, ok := parseSegmentName(name)
		if !ok {
			return nil, fmt.Errorf("logship: unparseable segment name %q for tenant %s", name, tenant)
		}
		segs = append(segs, segInfo{name: name, from: from, to: to})
	}
	sort.Slice(segs, func(i, j int) bool {
		return segs[i].from < segs[j].from
	})
	return segs, nil
}

// chainAfter verifies, using names only, that the segments ending after next
// form an unbroken chain from next to the last segment. Segments that end at
// or before next are ignored.
func chainAfter(tenant string, segs []segInfo, next uint64) error {
	for _, s := range segs {
		if s.to <= next {
			continue
		}
		if s.from != next {
			if s.from < next {
				return fmt.Errorf("logship: overlapping segment %q for tenant %s: begins at %d, expected %d", s.name, tenant, s.from, next)
			}
			return fmt.Errorf("logship: gap before segment %q for tenant %s: begins at %d, expected %d (a segment is missing)", s.name, tenant, s.from, next)
		}
		next = s.to
	}
	return nil
}

// replay is the body of Replay: it returns the horizon of the base it started
// from (the zero time when it replayed without a base). It picks the newest
// readable base whose following segments chain without a gap, falling back to
// older bases when a newer one is unreadable, and applies nothing before that
// choice is made.
func (sh *Shipper) replay(ctx context.Context, tenant string, apply func(model.Event) error) (time.Time, error) {
	baseSeqs, err := sh.bases(ctx, tenant)
	if err != nil {
		return time.Time{}, err
	}
	names, err := sh.segments(ctx, tenant)
	if err != nil {
		return time.Time{}, err
	}
	segs, err := sortedSegs(tenant, names)
	if err != nil {
		return time.Time{}, err
	}

	var (
		next       uint64 // expected `from` of the next segment; the log starts at 0
		horizon    time.Time
		fromBase   bool
		baseEvents []model.Event
	)
	var skippedNames []string
	var skippedErrs []error

	// Choose the starting point before applying anything so that a failure
	// never applies events from a base it then abandons. Bases are tried newest first: if
	// the chain after the newest base is broken, an older base cannot repair
	// it either, so the chain error is returned immediately.
	for i := len(baseSeqs) - 1; i >= 0; i-- {
		seq := baseSeqs[i]
		if err := chainAfter(tenant, segs, seq); err != nil {
			return time.Time{}, errors.Join(append([]error{err}, skippedErrs...)...)
		}
		hdr, events, readErr := sh.readBase(ctx, tenant, seq)
		if readErr != nil {
			skippedNames = append(skippedNames, baseName(tenant, seq))
			skippedErrs = append(skippedErrs, readErr)
			continue
		}
		next = seq
		horizon = hdr.Horizon
		fromBase = true
		baseEvents = events
		break
	}

	if len(baseSeqs) > 0 && !fromBase {
		// No base was readable; fall back to a chain from 0 only if the
		// segments reach all the way back to the start of the log. An empty
		// segment list is not a chain from 0: with a base present, it means the
		// history lives only in the unreadable bases, and replaying nothing
		// would be a silent empty restore.
		if len(segs) == 0 || segs[0].from != 0 {
			return time.Time{}, fmt.Errorf("logship: no usable starting point for tenant %s: %w", tenant, errors.Join(skippedErrs...))
		}
		if err := chainAfter(tenant, segs, 0); err != nil {
			return time.Time{}, fmt.Errorf("logship: no usable starting point for tenant %s: %w", tenant, errors.Join(skippedErrs...))
		}
	}

	if len(skippedErrs) > 0 {
		usedKey, usedVal := "used", "chain from 0"
		if fromBase {
			usedKey, usedVal = "used_base", baseName(tenant, next)
		}
		for i := range skippedErrs {
			slog.Default().Warn("logship: skipping unreadable base",
				"tenant", tenant,
				"skipped_base", skippedNames[i],
				"err", skippedErrs[i],
				usedKey, usedVal)
		}
	}

	for i := range baseEvents {
		if err := apply(baseEvents[i]); err != nil {
			return time.Time{}, err
		}
	}

	for _, s := range segs {
		if fromBase && s.to <= next {
			// segment entirely before the base
			continue
		}
		if s.from != next {
			if s.from < next {
				return time.Time{}, fmt.Errorf("logship: overlapping segment %q for tenant %s: begins at %d, expected %d", s.name, tenant, s.from, next)
			}
			return time.Time{}, fmt.Errorf("logship: gap before segment %q for tenant %s: begins at %d, expected %d (a segment is missing)", s.name, tenant, s.from, next)
		}
		data, err := sh.sink.Get(ctx, s.name)
		if err != nil {
			return time.Time{}, err
		}
		if strings.HasSuffix(s.name, segZstExt) {
			data, err = decompressSegment(data)
			if err != nil {
				return time.Time{}, fmt.Errorf("logship: decompressing segment %s: %w", s.name, err)
			}
		}
		events, err := decodeSegment(data)
		if err != nil {
			return time.Time{}, fmt.Errorf("logship: decoding segment %s: %w", s.name, err)
		}
		for i := range events {
			if err := apply(events[i]); err != nil {
				return time.Time{}, err
			}
		}
		next = s.to
	}
	return horizon, nil
}

// Tenants returns the distinct tenant ids that have segments in the sink, sorted
// — the set a restore would reconstruct.
func (sh *Shipper) Tenants(ctx context.Context) ([]string, error) {
	names, err := sh.sink.List(ctx, "")
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var out []string
	for _, n := range names {
		if !isSegmentName(n) {
			continue
		}
		i := strings.IndexByte(n, '/')
		if i <= 0 {
			continue
		}
		t := n[:i]
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

// lastShipped is the highest sequence already in the sink for the tenant,
// cached after the first derivation.
func (sh *Shipper) lastShipped(ctx context.Context, tenant string) (uint64, error) {
	sh.mu.Lock()
	if v, ok := sh.cursor[tenant]; ok {
		sh.mu.Unlock()
		return v, nil
	}
	sh.mu.Unlock()

	names, err := sh.segments(ctx, tenant)
	if err != nil {
		return 0, err
	}
	var maxSeq uint64
	for _, name := range names {
		if _, to, ok := parseSegmentName(name); ok && to > maxSeq {
			maxSeq = to
		}
	}
	baseSeqs, err := sh.bases(ctx, tenant)
	if err != nil {
		return 0, err
	}
	for _, seq := range baseSeqs {
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	sh.mu.Lock()
	sh.cursor[tenant] = maxSeq
	sh.mu.Unlock()
	return maxSeq, nil
}

// isSegmentName reports whether n ends with either segment extension.
func isSegmentName(n string) bool {
	return strings.HasSuffix(n, segExt) || strings.HasSuffix(n, segZstExt)
}

func (sh *Shipper) segments(ctx context.Context, tenant string) ([]string, error) {
	names, err := sh.sink.List(ctx, tenant+"/")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if isBaseName(n) {
			continue
		}
		if isSegmentName(n) {
			out = append(out, n)
		}
	}
	return out, nil
}

// segmentName is "<tenant>/<from>-<to>.seg.zst" with zero-padded hex sequences,
// so lexical order is sequence order.
func segmentName(tenant string, from, to uint64) string {
	return fmt.Sprintf("%s/%016x-%016x%s", tenant, from, to, segZstExt)
}

// parseSegmentName parses "<tenant>/<from>-<to>.seg" or ".seg.zst" names (the
// longer suffix is checked first so it is not mistaken for ".seg").
func parseSegmentName(name string) (from, to uint64, ok bool) {
	base := name
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	switch {
	case strings.HasSuffix(base, segZstExt):
		base = strings.TrimSuffix(base, segZstExt)
	case strings.HasSuffix(base, segExt):
		base = strings.TrimSuffix(base, segExt)
	default:
		return 0, 0, false
	}
	parts := strings.SplitN(base, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	f, err1 := strconv.ParseUint(parts[0], 16, 64)
	t, err2 := strconv.ParseUint(parts[1], 16, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return f, t, true
}

// encodeSegment frames events length-delimited (uint32 big-endian length, then
// the marshaled proto), the same wire shape the projection snapshot uses. A
// marshal failure is surfaced, never silently framed as a zero-length record —
// a corrupt segment would otherwise only surface (if at all) at restore time.
func encodeSegment(events []model.Event) ([]byte, error) {
	var buf []byte
	var lenbuf [4]byte
	for i := range events {
		b, err := proto.Marshal(events[i].ToProto())
		if err != nil {
			return nil, fmt.Errorf("logship: marshaling event %d of segment: %w", i, err)
		}
		binary.BigEndian.PutUint32(lenbuf[:], uint32(len(b)))
		buf = append(buf, lenbuf[:]...)
		buf = append(buf, b...)
	}
	return buf, nil
}

func decodeSegment(data []byte) ([]model.Event, error) {
	var events []model.Event
	for len(data) > 0 {
		if len(data) < 4 {
			return nil, fmt.Errorf("truncated length prefix")
		}
		n := binary.BigEndian.Uint32(data[:4])
		data = data[4:]
		if uint64(len(data)) < uint64(n) {
			return nil, fmt.Errorf("truncated record: want %d, have %d", n, len(data))
		}
		var pe toisev1.Event
		if err := proto.Unmarshal(data[:n], &pe); err != nil {
			return nil, fmt.Errorf("unmarshal record: %w", err)
		}
		events = append(events, model.EventFromProto(&pe))
		data = data[n:]
	}
	return events, nil
}
