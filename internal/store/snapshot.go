package store

import "context"

// Snapshotter generates point-in-time snapshots of the projected state so the
// log can be replayed from a snapshot rather than from the beginning.
//
// Phase 1 ships only this interface as a stub: there is no implementation, and
// the store does not require one. Snapshot generation and old-event archival
// are phase-2 work (see ADR 0013). The interface is declared now so the rest of
// the system can be written against it without rework later.
type Snapshotter interface {
	// Snapshot writes a snapshot up to the store's current sequence.
	Snapshot(ctx context.Context) error
}
