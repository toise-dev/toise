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
}

// DefaultConfig returns the phase-1 defaults: unlimited retention and a
// one-hour compaction interval.
func DefaultConfig() Config {
	return Config{
		RetentionMaxAge:    0,
		CompactionInterval: time.Hour,
	}
}
