package store

import (
	"fmt"
	"time"

	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"

	"github.com/toise-dev/toise/internal/model"
	toisev1 "github.com/toise-dev/toise/proto/toise/v1"
)

// CoalesceHeartbeats compacts the log by collapsing maximal runs of consecutive
// entity.unchanged heartbeats for the same entity down to their first and last
// record, deleting the redundant middle ones (primary record and index
// entries). Meaningful events and relation events are never touched. It returns
// the number of records removed. See ADR 0013.
//
// The scan runs on a pebble snapshot without s.mu, so ingestion never waits on
// maintenance (#115): every staged delete is a seq that existed at snapshot
// time and was a run middle then — a concurrent append can only extend a run
// past its kept tail, which the next pass collapses.
func (s *Store) CoalesceHeartbeats() (int, error) {
	s.maintMu.Lock()
	defer s.maintMu.Unlock()

	snap := s.db.NewSnapshot()
	defer func() { _ = snap.Close() }()

	type rec struct {
		seq           uint64
		ct            model.ChangeType
		eventTimeNano int64
	}
	perEntity := make(map[model.EntityID][]rec)
	err := snapshotScan(snap, func(seq uint64, _ []byte, ev model.Event) error {
		if ev.Entity != nil {
			perEntity[ev.Entity.Entity.ID] = append(perEntity[ev.Entity.Entity.ID],
				rec{seq: seq, ct: ev.Entity.ChangeType, eventTimeNano: ev.Entity.EventTime.UnixNano()})
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("scanning for coalescing: %w", err)
	}

	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()
	deleted := 0
	for id, recs := range perEntity {
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
			// Keep recs[i] (first) and recs[j] (last); drop the middle. The
			// scan already carried everything the index keys need — no
			// per-record re-read.
			for k := i + 1; k < j; k++ {
				r := recs[k]
				for _, key := range [][]byte{
					primaryKey(r.seq),
					entityKey(string(id), r.seq),
					typeKey(r.ct, r.seq),
					timeKey(r.eventTimeNano, r.seq),
				} {
					if err := batch.Delete(key, nil); err != nil {
						return 0, fmt.Errorf("staging delete of seq %d: %w", r.seq, err)
					}
				}
				deleted++
			}
			i = j + 1
		}
	}
	if deleted == 0 {
		return 0, nil
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, fmt.Errorf("committing coalesce: %w", err)
	}
	return deleted, nil
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
	s.maintMu.Lock()
	defer s.maintMu.Unlock()

	// Both passes run on one pebble snapshot without s.mu, so the hourly prune
	// never stalls ingestion (#115). Safety against concurrent appends: the
	// keep set protects what was the latest at snapshot time; a newer append
	// only supersedes it (keeping it is conservative), and events appended
	// after the snapshot are invisible here, hence never staged for deletion.
	snap := s.db.NewSnapshot()
	defer func() { _ = snap.Close() }()

	// Pass 1 — the keep set: the latest seq of every still-live entity and
	// relation. A delete/remove as the last event means it is not live.
	type last struct {
		seq   uint64
		alive bool
	}
	entLast := map[model.EntityID]last{}
	relLast := map[model.RelationID]last{}
	if serr := snapshotScan(snap, func(seq uint64, _ []byte, ev model.Event) error {
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
	// does not protect. The stored value IS the marshaled event, so its length
	// is the exact payload size — no re-marshal (and no per-event identity
	// hash) just to count bytes.
	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()
	if serr := snapshotScan(snap, func(seq uint64, raw []byte, ev model.Event) error {
		if _, kept := keep[seq]; kept {
			return nil
		}
		if !recordedAtOf(ev).Before(cutoff) {
			return nil
		}
		bytes += int64(len(raw))
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
	s.mu.Lock()
	s.prunedEvents += uint64(events)
	s.prunedBytes += uint64(bytes)
	s.mu.Unlock()
	return events, bytes, nil
}

// snapshotScan visits every event in append order on a stable snapshot of the
// log, passing the raw stored value alongside the decoded event. Maintenance
// scans use it so they never hold s.mu while reading.
func snapshotScan(snap *pebble.Snapshot, fn func(seq uint64, raw []byte, ev model.Event) error) error {
	prefix := []byte(primaryPrefix)
	iter, err := snap.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return fmt.Errorf("opening snapshot iterator: %w", err)
	}
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		seq := seqFromKeySuffix(iter.Key())
		var pe toisev1.Event
		if err := proto.Unmarshal(iter.Value(), &pe); err != nil {
			return fmt.Errorf("decoding seq %d: %w", seq, err)
		}
		if err := fn(seq, iter.Value(), model.EventFromProto(&pe)); err != nil {
			return err
		}
	}
	return iter.Error()
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
