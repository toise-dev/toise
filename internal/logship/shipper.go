package logship

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/toise-dev/toise/internal/model"
	toisev1 "github.com/toise-dev/toise/proto/toise/v1"
)

// EventSource is the slice of the event log the shipper needs: the current max
// sequence, and a tail scan in sequence order. *store.Store satisfies it.
type EventSource interface {
	Sequence() uint64
	ScanFrom(afterSeq uint64, fn func(seq uint64, ev model.Event) error) error
}

const segExt = ".seg"

// Shipper exports a tenant's event log to a Sink as immutable, contiguous
// segments. The cursor (last shipped sequence) is derived from the sink, so the
// shipper holds no durable state and a crash never duplicates or skips a
// segment: the next Ship re-derives where to resume.
type Shipper struct {
	sink Sink

	mu     sync.Mutex
	cursor map[string]uint64 // tenant -> last shipped seq (cache of the sink-derived value)
}

// New returns a Shipper writing to sink.
func New(sink Sink) *Shipper {
	return &Shipper{sink: sink, cursor: map[string]uint64{}}
}

// Ship exports the events (lastShipped, current] for one tenant as a single
// segment object and returns how many it shipped (0 when already current). The
// range is closed at the sequence read before the scan, so events appended
// during the scan ship on the next call.
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

	seg, err := encodeSegment(events)
	if err != nil {
		return 0, err
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
// silently reconstructing a divergent log.
func (sh *Shipper) Replay(ctx context.Context, tenant string, apply func(model.Event) error) error {
	names, err := sh.segments(ctx, tenant)
	if err != nil {
		return err
	}
	sort.Slice(names, func(i, j int) bool {
		fi, _, _ := parseSegmentName(names[i])
		fj, _, _ := parseSegmentName(names[j])
		return fi < fj
	})
	var next uint64 // expected `from` of the next segment; the log starts at 0
	for _, name := range names {
		from, to, ok := parseSegmentName(name)
		if !ok {
			return fmt.Errorf("logship: unparseable segment name %q for tenant %s", name, tenant)
		}
		if from != next {
			if from < next {
				return fmt.Errorf("logship: overlapping segment %q for tenant %s: begins at %d, expected %d", name, tenant, from, next)
			}
			return fmt.Errorf("logship: gap before segment %q for tenant %s: begins at %d, expected %d (a segment is missing)", name, tenant, from, next)
		}
		data, err := sh.sink.Get(ctx, name)
		if err != nil {
			return err
		}
		events, err := decodeSegment(data)
		if err != nil {
			return fmt.Errorf("logship: decoding segment %s: %w", name, err)
		}
		for i := range events {
			if err := apply(events[i]); err != nil {
				return err
			}
		}
		next = to
	}
	return nil
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
		if !strings.HasSuffix(n, segExt) {
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
	sh.mu.Lock()
	sh.cursor[tenant] = maxSeq
	sh.mu.Unlock()
	return maxSeq, nil
}

func (sh *Shipper) segments(ctx context.Context, tenant string) ([]string, error) {
	names, err := sh.sink.List(ctx, tenant+"/")
	if err != nil {
		return nil, err
	}
	out := names[:0]
	for _, n := range names {
		if strings.HasSuffix(n, segExt) {
			out = append(out, n)
		}
	}
	return out, nil
}

// segmentName is "<tenant>/<from>-<to>.seg" with zero-padded hex sequences, so
// lexical order is sequence order.
func segmentName(tenant string, from, to uint64) string {
	return fmt.Sprintf("%s/%016x-%016x%s", tenant, from, to, segExt)
}

func parseSegmentName(name string) (from, to uint64, ok bool) {
	base := name
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, segExt)
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
