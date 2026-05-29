package store

import (
	"fmt"

	"github.com/cockroachdb/pebble"

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
