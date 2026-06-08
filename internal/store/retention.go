package store

import (
	"fmt"
	"time"

	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"

	"github.com/toise-dev/toise/internal/model"
)

// CoalesceHeartbeats compacts the log by collapsing maximal runs of consecutive
// entity.unchanged heartbeats for the same entity down to their first and last
// record, deleting the redundant middle ones (primary record and index
// entries). Meaningful events and relation events are never touched. It returns
// the number of records removed. See ADR 0013.
func (s *Store) CoalesceHeartbeats() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	type rec struct {
		seq uint64
		ct  model.ChangeType
	}
	perEntity := make(map[model.EntityID][]rec)
	err := s.Scan(func(seq uint64, ev model.Event) error {
		if ev.Entity != nil {
			id := ev.Entity.Entity.ID
			perEntity[id] = append(perEntity[id], rec{seq: seq, ct: ev.Entity.ChangeType})
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("scanning for coalescing: %w", err)
	}

	var toDelete []uint64
	for _, recs := range perEntity {
		i := 0
		for i < len(recs) {
			if recs[i].ct != model.EntityUnchanged {
				i++
				continue
			}
			j := i
			for j+1 < len(recs) && recs[j+1].ct == model.EntityUnchanged {
				j++
			}
			// Keep recs[i] (first) and recs[j] (last); drop the middle.
			for k := i + 1; k < j; k++ {
				toDelete = append(toDelete, recs[k].seq)
			}
			i = j + 1
		}
	}
	if len(toDelete) == 0 {
		return 0, nil
	}

	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()
	for _, seq := range toDelete {
		ev, err := s.getBySeq(seq)
		if err != nil {
			return 0, err
		}
		ee := ev.Entity
		for _, key := range [][]byte{
			primaryKey(seq),
			entityKey(string(ee.Entity.ID), seq),
			typeKey(ee.ChangeType, seq),
			timeKey(ee.EventTime.UnixNano(), seq),
		} {
			if err := batch.Delete(key, nil); err != nil {
				return 0, fmt.Errorf("staging delete of seq %d: %w", seq, err)
			}
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, fmt.Errorf("committing coalesce: %w", err)
	}
	return len(toDelete), nil
}

// PruneOlderThan drops events recorded before cutoff to bound on-disk growth,
// while preserving the current-state projection: the latest event of every live
// entity and live relation is kept regardless of age (so a replay rebuilds the
// same graph), and only older, superseded events are removed. It returns the
// number of events and the approximate bytes pruned, and accumulates them into
// the prune counters. See ADR 0013 and #45.
//
// Pruning is by recorded_at (storage age), not event_time, so a retroactively
// recorded old fact is not immediately pruned. An asKnownAt query before the
// retention horizon necessarily returns a truncated view — see
// docs/operations/configuration.md.
func (s *Store) PruneOlderThan(cutoff time.Time) (events int, bytes int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Pass 1 — the keep set: the latest seq of every still-live entity and
	// relation. A delete/remove as the last event means it is not live.
	type last struct {
		seq   uint64
		alive bool
	}
	entLast := map[model.EntityID]last{}
	relLast := map[model.RelationID]last{}
	if serr := s.Scan(func(seq uint64, ev model.Event) error {
		switch {
		case ev.Entity != nil:
			entLast[ev.Entity.Entity.ID] = last{seq, ev.Entity.ChangeType != model.EntityDeleted}
		case ev.Relation != nil:
			relLast[ev.Relation.Relation.ID] = last{seq, ev.Relation.ChangeType != model.RelationRemoved}
		}
		return nil
	}); serr != nil {
		return 0, 0, fmt.Errorf("scanning for keep set: %w", serr)
	}
	keep := make(map[uint64]struct{}, len(entLast)+len(relLast))
	for _, l := range entLast {
		if l.alive {
			keep[l.seq] = struct{}{}
		}
	}
	for _, l := range relLast {
		if l.alive {
			keep[l.seq] = struct{}{}
		}
	}

	// Pass 2 — stage deletes for events recorded before cutoff that the keep set
	// does not protect.
	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()
	if serr := s.Scan(func(seq uint64, ev model.Event) error {
		if _, kept := keep[seq]; kept {
			return nil
		}
		if !recordedAtOf(ev).Before(cutoff) {
			return nil
		}
		bytes += int64(proto.Size(ev.ToProto()))
		events++
		for _, key := range eventKeys(ev, seq) {
			if derr := batch.Delete(key, nil); derr != nil {
				return fmt.Errorf("staging delete of seq %d: %w", seq, derr)
			}
		}
		return nil
	}); serr != nil {
		return 0, 0, serr
	}
	if events == 0 {
		return 0, 0, nil
	}
	if cerr := batch.Commit(pebble.Sync); cerr != nil {
		return 0, 0, fmt.Errorf("committing prune: %w", cerr)
	}
	s.prunedEvents += uint64(events)
	s.prunedBytes += uint64(bytes)
	return events, bytes, nil
}

// recordedAtOf returns the event's ingestion timestamp (the storage-age clock).
func recordedAtOf(ev model.Event) time.Time {
	switch {
	case ev.Entity != nil:
		return ev.Entity.RecordedAt
	case ev.Relation != nil:
		return ev.Relation.RecordedAt
	}
	return time.Time{}
}

// eventKeys returns the primary and secondary index keys an event occupies — the
// exact set indexEvent wrote — so a prune removes every trace of it.
func eventKeys(ev model.Event, seq uint64) [][]byte {
	keys := [][]byte{primaryKey(seq)}
	switch {
	case ev.Entity != nil:
		ee := ev.Entity
		keys = append(keys, entityKey(string(ee.Entity.ID), seq),
			typeKey(ee.ChangeType, seq), timeKey(ee.EventTime.UnixNano(), seq))
	case ev.Relation != nil:
		re := ev.Relation
		keys = append(keys, entityKey(string(re.Relation.From), seq), entityKey(string(re.Relation.To), seq),
			typeKey(re.ChangeType, seq), timeKey(re.EventTime.UnixNano(), seq))
	}
	return keys
}

// PrunedEvents and PrunedBytes return the cumulative retention-prune counters,
// for the toise_*_pruned_total metrics.
func (s *Store) PrunedEvents() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prunedEvents
}

func (s *Store) PrunedBytes() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prunedBytes
}
