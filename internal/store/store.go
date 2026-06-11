package store

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"

	"github.com/toise-dev/toise/internal/model"
	toisev1 "github.com/toise-dev/toise/proto/toise/v1"
)

// Key prefixes. Primary records hold the serialized event; the others are
// secondary indexes whose values are empty and whose key suffix is the
// 8-byte big-endian sequence of the primary record.
const (
	primaryPrefix = "evt/"
	entityPrefix  = "ent/"
	typePrefix    = "typ/"
	timePrefix    = "tim/"
)

var metaSeqKey = []byte("meta/seq")

// metaPruneHorizonKey persists the latest retention cutoff ever applied: events
// recorded before it may be gone, so an as-of read older than it cannot be
// answered completely (#135).
var metaPruneHorizonKey = []byte("meta/prune_horizon")

// metaFormatKey marks the on-disk format version, so a future format change can
// refuse or migrate explicitly instead of misreading the keys (#144). Stores
// written before the marker existed are format 1 by definition.
var metaFormatKey = []byte("meta/format_version")

// formatVersion is the current on-disk format.
const formatVersion = uint64(1)

// Store is the append-only event log backed by Pebble. It is safe for
// concurrent use; appends are serialized.
type Store struct {
	db       *pebble.DB
	cfg      Config
	dir      string
	readOnly bool

	mu               sync.Mutex // guards seq/counters and serializes appends
	maintMu          sync.Mutex // serializes maintenance (coalesce/prune) against itself, NOT against appends
	seq              uint64     // last assigned sequence
	pruneHorizon     int64      // unix nanos of the latest prune cutoff (0 = never pruned)
	prunedEvents     uint64     // cumulative events removed by retention pruning
	prunedBytes      uint64     // cumulative approximate bytes removed by pruning
	snapshotSeq      uint64     // reference sequence of the last written snapshot
	snapshotsWritten uint64     // cumulative snapshots written
}

// Open opens (creating if needed) the event log at dir.
func Open(dir string, cfg Config) (*Store, error) {
	return open(dir, cfg, false)
}

// OpenReadOnly opens an existing event log at dir without ever writing to it:
// no directory creation, no format-version stamp, and every mutation fails
// with pebble's read-only error. It is the open for offline tooling — taking a
// backup must not be able to alter or mint the store it backs up.
func OpenReadOnly(dir string, cfg Config) (*Store, error) {
	return open(dir, cfg, true)
}

func open(dir string, cfg Config, readOnly bool) (*Store, error) {
	db, err := pebble.Open(dir, &pebble.Options{ReadOnly: readOnly})
	if err != nil {
		return nil, fmt.Errorf("opening pebble at %s: %w", dir, err)
	}
	s := &Store{db: db, cfg: cfg, dir: dir, readOnly: readOnly}
	if err := s.checkFormat(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.recoverSeq(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// checkFormat stamps a fresh (or pre-marker) store with the current format
// version and refuses one written by a NEWER format — misreading it would
// corrupt silently; the error tells the operator which binary to use.
func (s *Store) checkFormat() error {
	v, closer, err := s.db.Get(metaFormatKey)
	if errors.Is(err, pebble.ErrNotFound) {
		if s.readOnly {
			// A pre-marker store is format 1 by definition; a read-only open
			// reads it as such instead of stamping it.
			return nil
		}
		return s.db.Set(metaFormatKey, encodeU64(formatVersion), pebble.Sync)
	}
	if err != nil {
		return fmt.Errorf("reading format version: %w", err)
	}
	defer func() { _ = closer.Close() }()
	if len(v) == 8 {
		if got := binary.BigEndian.Uint64(v); got > formatVersion {
			return fmt.Errorf("store format version %d is newer than this binary supports (%d): upgrade toise-server before opening this data dir", got, formatVersion)
		}
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// DiskUsage returns the store's approximate on-disk size in bytes, for the
// store_disk_bytes metric.
func (s *Store) DiskUsage() uint64 {
	return s.db.Metrics().DiskSpaceUsage()
}

// Healthy reports whether the store is operational by issuing a light read.
// pebble.ErrNotFound is healthy (a never-written store); any other error — a
// closed or broken DB — is not. Used by the /readyz probe.
func (s *Store) Healthy() error {
	_, closer, err := s.db.Get(metaSeqKey)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("store health check: %w", err)
	}
	return closer.Close()
}

// recoverSeq loads the persisted sequence so appends continue monotonically
// after a restart or crash.
func (s *Store) recoverSeq() error {
	v, closer, err := s.db.Get(metaSeqKey)
	if errors.Is(err, pebble.ErrNotFound) {
		s.seq = 0
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading sequence: %w", err)
	}
	defer func() { _ = closer.Close() }()
	if len(v) == 8 {
		s.seq = binary.BigEndian.Uint64(v)
	}
	if hv, hcloser, herr := s.db.Get(metaPruneHorizonKey); herr == nil {
		if len(hv) == 8 {
			s.pruneHorizon = int64(binary.BigEndian.Uint64(hv))
		}
		_ = hcloser.Close()
	} else if !errors.Is(herr, pebble.ErrNotFound) {
		return fmt.Errorf("reading prune horizon: %w", herr)
	}
	return nil
}

// PruneHorizon returns the latest retention cutoff ever applied (zero if the
// store has never pruned): the oldest instant an as-of read can answer
// completely.
func (s *Store) PruneHorizon() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pruneHorizon == 0 {
		return time.Time{}
	}
	return time.Unix(0, s.pruneHorizon).UTC()
}

// Append durably writes the given events to the log in a single atomic batch
// (committed with Sync). Each event is validated first.
func (s *Store) Append(events ...model.Event) error {
	if len(events) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()

	localSeq := s.seq
	validate := func(ev model.Event) error { return ev.Validate() }
	if s.cfg.AcceptUnknownTypes {
		// Open vocabulary (#141): shape must still be sound, membership not.
		validate = func(ev model.Event) error { return ev.ValidateShape() }
	}
	for i := range events {
		ev := events[i]
		if err := validate(ev); err != nil {
			return fmt.Errorf("event %d invalid: %w", i, err)
		}
		localSeq++
		data, err := proto.Marshal(ev.ToProto())
		if err != nil {
			return fmt.Errorf("marshaling event %d: %w", i, err)
		}
		if err := batch.Set(primaryKey(localSeq), data, nil); err != nil {
			return fmt.Errorf("staging event %d: %w", i, err)
		}
		if err := indexEvent(batch, ev, localSeq); err != nil {
			return fmt.Errorf("indexing event %d: %w", i, err)
		}
	}
	if err := batch.Set(metaSeqKey, encodeU64(localSeq), nil); err != nil {
		return fmt.Errorf("staging sequence: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("committing %d events: %w", len(events), err)
	}
	s.seq = localSeq
	return nil
}

// Sequence returns the last assigned sequence (the number of records ever
// appended, modulo coalescing).
func (s *Store) Sequence() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq
}

// indexEvent stages the secondary index entries for an event. Relation events
// are indexed under both endpoint entity IDs.
func indexEvent(batch *pebble.Batch, ev model.Event, seq uint64) error {
	var (
		ct       model.ChangeType
		eventNs  int64
		entities []string
	)
	switch {
	case ev.Entity != nil:
		ct = ev.Entity.ChangeType
		eventNs = ev.Entity.EventTime.UnixNano()
		entities = []string{string(ev.Entity.Entity.ID)}
	case ev.Relation != nil:
		ct = ev.Relation.ChangeType
		eventNs = ev.Relation.EventTime.UnixNano()
		entities = []string{string(ev.Relation.Relation.From), string(ev.Relation.Relation.To)}
	default:
		return errors.New("store: empty event envelope")
	}
	for _, id := range entities {
		if id == "" {
			continue
		}
		if err := batch.Set(entityKey(id, seq), nil, nil); err != nil {
			return err
		}
	}
	if err := batch.Set(typeKey(ct, seq), nil, nil); err != nil {
		return err
	}
	return batch.Set(timeKey(eventNs, seq), nil, nil)
}

// getBySeq fetches and decodes the primary record for seq.
func (s *Store) getBySeq(seq uint64) (model.Event, error) {
	v, closer, err := s.db.Get(primaryKey(seq))
	if err != nil {
		return model.Event{}, fmt.Errorf("reading seq %d: %w", seq, err)
	}
	defer func() { _ = closer.Close() }()
	var pe toisev1.Event
	if err := proto.Unmarshal(v, &pe); err != nil {
		return model.Event{}, fmt.Errorf("decoding seq %d: %w", seq, err)
	}
	return model.EventFromProto(&pe), nil
}

// --- key helpers ---

func encodeU64(n uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, n)
	return b
}

func primaryKey(seq uint64) []byte {
	return append([]byte(primaryPrefix), encodeU64(seq)...)
}

func entityKey(id string, seq uint64) []byte {
	k := make([]byte, 0, len(entityPrefix)+len(id)+1+8)
	k = append(k, entityPrefix...)
	k = append(k, id...)
	k = append(k, '/')
	return append(k, encodeU64(seq)...)
}

func typeKey(ct model.ChangeType, seq uint64) []byte {
	k := make([]byte, 0, len(typePrefix)+1+8)
	k = append(k, typePrefix...)
	k = append(k, byte(ct))
	return append(k, encodeU64(seq)...)
}

func timeKey(eventNano int64, seq uint64) []byte {
	k := make([]byte, 0, len(timePrefix)+8+8)
	k = append(k, timePrefix...)
	k = append(k, encodeU64(uint64(eventNano))...)
	return append(k, encodeU64(seq)...)
}

// seqFromKeySuffix extracts the trailing 8-byte sequence from an index key.
func seqFromKeySuffix(key []byte) uint64 {
	if len(key) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(key[len(key)-8:])
}

// prefixUpperBound returns the smallest key greater than every key with the
// given prefix, for use as an iterator UpperBound.
func prefixUpperBound(prefix []byte) []byte {
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] != 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	return nil // prefix is all 0xff: no upper bound
}
