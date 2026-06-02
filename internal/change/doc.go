// Package change is Toise's change-detection engine. It turns raw observations
// (an entity's current state, a relation) into classified, qualified events.
//
// For each observation it diffs against the current projection, classifies the
// change per the taxonomy (ADR 0006), assigns the logical entity ID by exact
// identity matching — identity is immutable (ADR 0018, superseding ADR 0017), so
// a differing identity is a different entity, never a fuzzy merge — appends the
// qualified event to the store, applies it to the projection, and notifies
// subscribers.
// Structural relation add/remove (ADR 0004) are flagged as high-priority
// signals suitable for alerting.
package change
