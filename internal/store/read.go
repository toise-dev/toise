package store

import (
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
// (for relation events, either endpoint).
func (s *Store) ReadByEntity(id model.EntityID) ([]model.Event, error) {
	prefix := append([]byte(entityPrefix), append([]byte(id), '/')...)
	return s.readBySeqIndex(prefix)
}

// ReadByType returns, in append order, every event of the given change type.
func (s *Store) ReadByType(ct model.ChangeType) ([]model.Event, error) {
	prefix := append([]byte(typePrefix), byte(ct))
	return s.readBySeqIndex(prefix)
}

// ReadByTimeRange returns events whose event_time is in [start, end), ordered by
// event_time then sequence.
func (s *Store) ReadByTimeRange(start, end time.Time) ([]model.Event, error) {
	lower := timeKeyBound(start.UnixNano())
	upper := timeKeyBound(end.UnixNano())
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil, fmt.Errorf("opening time iterator: %w", err)
	}
	defer func() { _ = iter.Close() }()

	var out []model.Event
	for iter.First(); iter.Valid(); iter.Next() {
		ev, err := s.getBySeq(seqFromKeySuffix(iter.Key()))
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return out, nil
}

// readBySeqIndex iterates an index prefix and resolves each entry to its event.
func (s *Store) readBySeqIndex(prefix []byte) ([]model.Event, error) {
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return nil, fmt.Errorf("opening index iterator: %w", err)
	}
	defer func() { _ = iter.Close() }()

	var out []model.Event
	for iter.First(); iter.Valid(); iter.Next() {
		ev, err := s.getBySeq(seqFromKeySuffix(iter.Key()))
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return out, nil
}

// timeKeyBound builds a time-index bound key (prefix + event_time nanos) without
// a sequence suffix, for use as an iterator lower/upper bound.
func timeKeyBound(eventNano int64) []byte {
	return append([]byte(timePrefix), encodeU64(uint64(eventNano))...)
}
