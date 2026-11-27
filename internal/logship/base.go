// base.go: BASE segment snapshots. A base is a full copy of a tenant's event
// log up to sequence S. Once a base exists, older regular segments (and older
// bases) below the retention cut can be deleted, because the base alone is
// sufficient to replay history up to S.

package logship

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

// errStopScan aborts a ScanFrom callback early; it is detected via errors.Is so
// wrappers added by the source do not hide it.
var errStopScan = errors.New("logship: stop scan")

const (
	basePrefix    = "base-"
	baseHeaderLen = 21
	baseMagic     = "TBSE"
	baseVersion   = byte(1)
)

func baseName(tenant string, seq uint64) string {
	return fmt.Sprintf("%s/%s%016x%s", tenant, basePrefix, seq, segZstExt)
}

func parseBaseName(name string) (seq uint64, ok bool) {
	elem := path.Base(name)
	if elem == "." || elem == "/" {
		return 0, false
	}
	if !strings.HasPrefix(elem, basePrefix) || !strings.HasSuffix(elem, segZstExt) {
		return 0, false
	}
	hex := strings.TrimSuffix(strings.TrimPrefix(elem, basePrefix), segZstExt)
	if hex == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(hex, 16, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func isBaseName(name string) bool {
	_, ok := parseBaseName(name)
	return ok
}

// baseHeader is serialized at the START of the object, UNCOMPRESSED, as:
//
//	"TBSE" | version byte (1) | horizon int64 BE unix nanos (0 == zero Time)
//	       | writtenAt int64 BE unix nanos
//
// The fixed part is baseHeaderLen bytes; it is followed by
// compressSegment(encodeSegment(events)), i.e. the zstd-compressed framed
// events. Keeping the header raw means the retention cut can be found with a
// small ranged read instead of downloading and decompressing whole bases.
type baseHeader struct {
	Horizon   time.Time
	WrittenAt time.Time
}

func timeToNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func nanosToTime(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

// parseBaseHeader parses and validates the fixed baseHeaderLen-byte prefix of
// a base object. It never touches the compressed payload.
func parseBaseHeader(raw []byte) (baseHeader, error) {
	if len(raw) < baseHeaderLen {
		return baseHeader{}, fmt.Errorf("logship: base too short: %d bytes", len(raw))
	}
	if string(raw[:4]) != baseMagic {
		return baseHeader{}, fmt.Errorf("logship: bad base magic %q", raw[:4])
	}
	if v := raw[4]; v != baseVersion {
		return baseHeader{}, fmt.Errorf("logship: unsupported base version %d (want version 1)", v)
	}
	return baseHeader{
		Horizon:   nanosToTime(int64(binary.BigEndian.Uint64(raw[5:13]))),
		WrittenAt: nanosToTime(int64(binary.BigEndian.Uint64(raw[13:21]))),
	}, nil
}

func encodeBase(h baseHeader, events []model.Event) ([]byte, error) {
	body, err := encodeSegment(events)
	if err != nil {
		return nil, fmt.Errorf("logship: encode base events: %w", err)
	}
	compressed, err := compressSegment(body)
	if err != nil {
		return nil, fmt.Errorf("logship: compress base: %w", err)
	}
	buf := make([]byte, baseHeaderLen, baseHeaderLen+len(compressed))
	copy(buf, baseMagic)
	buf[4] = baseVersion
	binary.BigEndian.PutUint64(buf[5:13], uint64(timeToNanos(h.Horizon)))
	binary.BigEndian.PutUint64(buf[13:21], uint64(timeToNanos(h.WrittenAt)))
	buf = append(buf, compressed...)
	return buf, nil
}

func decodeBase(data []byte) (baseHeader, []model.Event, error) {
	h, err := parseBaseHeader(data)
	if err != nil {
		return baseHeader{}, nil, err
	}
	raw, err := decompressSegment(data[baseHeaderLen:])
	if err != nil {
		return baseHeader{}, nil, fmt.Errorf("logship: decompress base: %w", err)
	}
	events, err := decodeSegment(raw)
	if err != nil {
		return baseHeader{}, nil, fmt.Errorf("logship: decode base events: %w", err)
	}
	return h, events, nil
}

func (sh *Shipper) bases(ctx context.Context, tenant string) ([]uint64, error) {
	names, err := sh.sink.List(ctx, tenant+"/")
	if err != nil {
		return nil, fmt.Errorf("logship: list bases for %s: %w", tenant, err)
	}
	seqs := make([]uint64, 0, len(names))
	for _, name := range names {
		if seq, ok := parseBaseName(name); ok {
			seqs = append(seqs, seq)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	return seqs, nil
}

func (sh *Shipper) readBase(ctx context.Context, tenant string, seq uint64) (baseHeader, []model.Event, error) {
	name := baseName(tenant, seq)
	data, err := sh.sink.Get(ctx, name)
	if err != nil {
		return baseHeader{}, nil, fmt.Errorf("logship: get %s: %w", name, err)
	}
	h, events, err := decodeBase(data)
	if err != nil {
		return baseHeader{}, nil, fmt.Errorf("logship: decode %s: %w", name, err)
	}
	return h, events, nil
}

// readBaseHeader fetches only the fixed uncompressed header of a base, so
// retention decisions never download or decompress the events payload.
func (sh *Shipper) readBaseHeader(ctx context.Context, tenant string, seq uint64) (baseHeader, error) {
	name := baseName(tenant, seq)
	raw, err := sh.sink.GetPrefix(ctx, name, baseHeaderLen)
	if err != nil {
		return baseHeader{}, fmt.Errorf("logship: get %s header: %w", name, err)
	}
	h, err := parseBaseHeader(raw)
	if err != nil {
		return baseHeader{}, fmt.Errorf("logship: decode %s header: %w", name, err)
	}
	return h, nil
}

// Compact writes a base segment for the tenant when retention is positive and
// the newest base is older than baseInterval, then deletes the segments and
// bases that a base at least retention old makes redundant. It never ships:
// call Ship first.
func (sh *Shipper) Compact(ctx context.Context, tenant string, src EventSource, retention, baseInterval time.Duration) error {
	if retention <= 0 {
		return nil
	}
	now := sh.now()

	existing, err := sh.bases(ctx, tenant)
	if err != nil {
		return err
	}

	// Determine the age of the newest known base. lastBaseAt is a cache; if we
	// have no entry (first run after restart), read the newest base's header
	// once and cache it so repeated Compact calls do not re-download it.
	sh.mu.Lock()
	newestAt := sh.lastBaseAt[tenant]
	sh.mu.Unlock()
	var newestSeq uint64
	if len(existing) > 0 {
		newestSeq = existing[len(existing)-1]
	}
	if newestAt.IsZero() && len(existing) > 0 {
		h, readErr := sh.readBaseHeader(ctx, tenant, newestSeq)
		if readErr != nil {
			return readErr
		}
		newestAt = h.WrittenAt
		sh.mu.Lock()
		sh.lastBaseAt[tenant] = newestAt
		sh.mu.Unlock()
	}

	S, err := sh.lastShipped(ctx, tenant)
	if err != nil {
		return err
	}

	if S > 0 && (len(existing) == 0 || (now.Sub(newestAt) >= baseInterval && S > newestSeq)) {
		var events []model.Event
		scanErr := src.ScanFrom(0, func(seq uint64, ev model.Event) error {
			if seq > S {
				return errStopScan
			}
			events = append(events, ev)
			return nil
		})
		if scanErr != nil && !errors.Is(scanErr, errStopScan) {
			return fmt.Errorf("logship: scan %s for base: %w", tenant, scanErr)
		}

		// PruneHorizon is read only after the scan: if a prune runs concurrently
		// and removes events, the base must never claim a horizon newer than
		// the history it actually captured.
		horizon := src.PruneHorizon()

		data, encErr := encodeBase(baseHeader{Horizon: horizon, WrittenAt: now}, events)
		if encErr != nil {
			return encErr
		}
		name := baseName(tenant, S)
		if putErr := sh.sink.Put(ctx, name, data); putErr != nil {
			return fmt.Errorf("logship: put %s: %w", name, putErr)
		}

		// Read-back verification: replay always starts from the newest base,
		// so an unreadable or inconsistent base must never be left behind.
		_, got, verifyErr := sh.readBase(ctx, tenant, S)
		if verifyErr == nil && len(got) != len(events) {
			verifyErr = fmt.Errorf("logship: %s: event count mismatch: got %d, wrote %d", name, len(got), len(events))
		}
		if verifyErr != nil {
			if delErr := sh.sink.Delete(ctx, name); delErr != nil {
				return errors.Join(verifyErr, fmt.Errorf("logship: removing unverified base %s: %w", name, delErr))
			}
			return verifyErr
		}

		sh.mu.Lock()
		sh.lastBaseAt[tenant] = now
		sh.mu.Unlock()
	} else {
		// Deletion only happens in a call that wrote a base, so a new base
		// always exists before anything below the retention cut is removed.
		return nil
	}

	bases, err := sh.bases(ctx, tenant)
	if err != nil {
		return err
	}

	// Walk newest to oldest; the cut is the NEWEST base old enough for
	// retention, so we never delete history covered by a fresh base. Only the
	// fixed header of each base is read, not its events payload.
	cut := uint64(0)
	found := false
	for i := len(bases) - 1; i >= 0; i-- {
		h, readErr := sh.readBaseHeader(ctx, tenant, bases[i])
		if readErr != nil {
			return readErr
		}
		if now.Sub(h.WrittenAt) >= retention {
			cut = bases[i]
			found = true
			break
		}
	}
	if !found {
		return nil
	}

	segs, err := sh.segments(ctx, tenant)
	if err != nil {
		return err
	}
	for _, seg := range segs {
		_, to, ok := parseSegmentName(seg)
		if ok && to <= cut {
			if err := sh.sink.Delete(ctx, seg); err != nil {
				return fmt.Errorf("logship: delete %s: %w", seg, err)
			}
		}
	}
	for _, seq := range bases {
		if seq < cut {
			if err := sh.sink.Delete(ctx, baseName(tenant, seq)); err != nil {
				return fmt.Errorf("logship: delete %s: %w", baseName(tenant, seq), err)
			}
		}
	}
	return nil
}
