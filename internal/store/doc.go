// Package store implements Toise's durable, append-only event log on top of
// Pebble (see ADR 0007). The log is the system's source of truth; the graph
// projection is derived from it.
//
// Events are keyed by a monotonic sequence (append/ingestion order). Secondary
// indexes support reads by entity ID, by change type, and by event_time range.
// Appends are atomic and durable: a batch's primary record, its index entries,
// and the persisted sequence are written in a single Pebble batch committed
// with Sync.
//
// Retention (ADR 0013): meaningful events are kept; runs of consecutive
// entity.unchanged heartbeats can be coalesced via CoalesceHeartbeats, and
// events past the configured horizon are pruned by compaction. Snapshots
// persist the projection (see SnapshotStore) so a restart replays only the tail
// rather than the whole log.
package store
