package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"

	"github.com/toise-dev/toise/internal/model"
	toisev1 "github.com/toise-dev/toise/proto/toise/v1"
)

// Scan visits every event in append (ingestion) order. It is the primary read
// path for rebuilding the projection at startup. The callback must not retain
// the event beyond the call. A callback error aborts the scan and is returned.
func (s *Store) Scan(fn func(seq uint64, ev model.Event) error) error {
	prefix := []byte(primaryPrefix)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return fmt.Errorf("opening scan iterator: %w", err)
	}
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		seq := seqFromKeySuffix(iter.Key())
		var pe toisev1.Event
		if err := proto.Unmarshal(iter.Value(), &pe); err != nil {
			return fmt.Errorf("decoding seq %d: %w", seq, err)
		}
		if err := fn(seq, model.EventFromProto(&pe)); err != nil {
			return err
		}
	}
	return iter.Error()
}

// ReadByEntity returns, in append order, every event whose entity ID matches id
// (for relation events, either endpoint). The context cancels a long read — the
// query surfaces bound their calls with per-request deadlines.
func (s *Store) ReadByEntity(ctx context.Context, id model.EntityID) ([]model.Event, error) {
	prefix := append([]byte(entityPrefix), append([]byte(id), '/')...)
	return s.readBySeqIndex(ctx, prefix)
}

// ReadByType returns, in append order, every event of the given change type.
func (s *Store) ReadByType(ctx context.Context, ct model.ChangeType) ([]model.Event, error) {
	prefix := append([]byte(typePrefix), byte(ct))
	return s.readBySeqIndex(ctx, prefix)
}

// ReadByTimeRange returns events whose event_time is in [start, end), ordered by
// event_time then sequence. The context cancels a long read.
func (s *Store) ScanByTimeRange(ctx context.Context, start, end time.Time, fn func(model.Event) error) error {
	lower := timeKeyBound(start.UnixNano())
	upper := timeKeyBound(end.UnixNano())
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return fmt.Errorf("opening time iterator: %w", err)
	}
	defer func() { _ = iter.Close() }()

	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		if err := checkEvery(ctx, n); err != nil {
			return err
		}
		n++
		ev, ok, err := s.resolveSeq(seqFromKeySuffix(iter.Key()))
		if err != nil {
			return err
		}
		if ok {
			if err := fn(ev); err != nil {
				return err
			}
		}
	}
	return iter.Error()
}

// ReadByTimeRange returns, in event-time order, every event whose event_time is
// in [start, end). It materializes the whole range; prefer ScanByTimeRange when
// folding (it applies events as they stream, with no intermediate slice). Kept
// for callers that genuinely need the full slice (recent_changes).
func (s *Store) ReadByTimeRange(ctx context.Context, start, end time.Time) ([]model.Event, error) {
	var out []model.Event
	if err := s.ScanByTimeRange(ctx, start, end, func(ev model.Event) error {
		out = append(out, ev)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// readBySeqIndex iterates an index prefix and resolves each entry to its event.
func (s *Store) readBySeqIndex(ctx context.Context, prefix []byte) ([]model.Event, error) {
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return nil, fmt.Errorf("opening index iterator: %w", err)
	}
	defer func() { _ = iter.Close() }()

	var out []model.Event
	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		if err := checkEvery(ctx, n); err != nil {
			return nil, err
		}
		n++
		ev, ok, err := s.resolveSeq(seqFromKeySuffix(iter.Key()))
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, ev)
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return out, nil
}

// resolveSeq fetches the event behind an index entry; ok=false means the entry
// is dangling and must be skipped, not failed. The index iterator is pinned to
// a point-in-time view, but getBySeq reads live, so a maintenance batch
// (CoalesceHeartbeats/PruneOlderThan) committing in between leaves visible
// index keys whose primary record is gone. Maintenance is the ONLY deleter of
// primary records and always removes primary+index keys in one atomic batch, so
// a missing primary deterministically means coalesced or pruned history — never
// a live event.
func (s *Store) resolveSeq(seq uint64) (model.Event, bool, error) {
	ev, err := s.getBySeq(seq)
	if errors.Is(err, pebble.ErrNotFound) {
		return model.Event{}, false, nil
	}
	if err != nil {
		return model.Event{}, false, err
	}
	return ev, true, nil
}

// checkEvery surfaces context cancellation once per batch of iterations: often
// enough to stop a runaway read promptly, rare enough to stay off the hot cost.
func checkEvery(ctx context.Context, n int) error {
	const batch = 1024
	if n%batch == 0 {
		return ctx.Err()
	}
	return nil
}

// timeKeyBound builds a time-index bound key (prefix + event_time nanos) without
// a sequence suffix, for use as an iterator lower/upper bound. The unsigned
// encoding is persisted on the write side, so a negative eventNano wraps above
// every real key — callers must reject pre-epoch instants before getting here.
func timeKeyBound(eventNano int64) []byte {
	return append([]byte(timePrefix), encodeU64(uint64(eventNano))...)
}
