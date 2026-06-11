package store

import "time"

// Config holds the operator-tunable retention knobs. See ADR 0013.
type Config struct {
	// RetentionMaxAge bounds how long events are kept. Zero means unlimited
	// (the phase-1 default). Enforcement of max-age pruning is phase-2 work; the
	// knob is exposed now.
	RetentionMaxAge time.Duration
	// CompactionInterval is how often heartbeat coalescing runs when the server
	// schedules it. The default is one hour.
	CompactionInterval time.Duration
	// AcceptUnknownTypes relaxes vocabulary membership at the append boundary:
	// events whose entity/relation type is not in the built-in registry are
	// accepted as long as their SHAPE is sound (identity present, well-formed
	// key-values). Off by default — the strict vocabulary (#141).
	AcceptUnknownTypes bool
}

// DefaultConfig returns the phase-1 defaults: unlimited retention and a
// one-hour compaction interval.
func DefaultConfig() Config {
	return Config{
		RetentionMaxAge:    0,
		CompactionInterval: time.Hour,
	}
}
