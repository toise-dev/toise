// Package projection maintains Toise's live graph as an in-memory projection of
// the event log (see ADR 0008). It is derived state: discard it and replay the
// log to rebuild it.
//
// The graph stores entities, relations, and outgoing/incoming adjacency, plus
// auxiliary indexes (identity hash -> logical ID, type -> ids) used by change
// detection. It is rebuilt at startup via Replay and updated live via Apply.
// Reads are concurrent-safe under a sync.RWMutex.
//
// "Current state" queries read this projection; bi-temporal history queries
// (ADR 0005) are served from the log, not from here.
package projection
