package change

import (
	"time"
)

// ProducerSeen records one speaker (producer and collection method) and the
// engine-clock time it last spoke.
type ProducerSeen struct {
	Producer string
	Scope    string
	LastSeen time.Time
}

// Caller holds obsMu.
func (e *Engine) noteSpoke(producer, scope string) {
	e.freshMu.Lock()
	defer e.freshMu.Unlock()
	if e.lastSpoke == nil {
		e.lastSpoke = make(map[string]time.Time)
	}
	e.lastSpoke[refKey(producer, scope)] = e.now()
}

// Caller holds obsMu. Runs once per sweep so the table stays bounded by the
// speakers that still hold a reference, without scanning at scrape time.
func (e *Engine) pruneSpeakers() {
	live := make(map[string]struct{})
	for _, byKey := range e.refs {
		for key := range byKey {
			live[key] = struct{}{}
		}
	}
	for _, byKey := range e.relRefs {
		for key := range byKey {
			live[key] = struct{}{}
		}
	}
	e.freshMu.Lock()
	defer e.freshMu.Unlock()
	for key := range e.lastSpoke {
		if _, ok := live[key]; !ok {
			delete(e.lastSpoke, key)
		}
	}
}

// Caller holds obsMu.
func (e *Engine) reseedSpeakers(candidates map[string]time.Time) {
	e.freshMu.Lock()
	defer e.freshMu.Unlock()
	if e.lastSpoke == nil && len(candidates) > 0 {
		e.lastSpoke = make(map[string]time.Time, len(candidates))
	}
	for key, c := range candidates {
		if prev, ok := e.lastSpoke[key]; !ok || prev.Before(c) {
			e.lastSpoke[key] = c
		}
	}
}

// ProducerLastSeen takes only freshMu, never obsMu, so a scrape never waits
// on an observation batch.
func (e *Engine) ProducerLastSeen() []ProducerSeen {
	e.freshMu.Lock()
	defer e.freshMu.Unlock()
	if len(e.lastSpoke) == 0 {
		return nil
	}
	seen := make([]ProducerSeen, 0, len(e.lastSpoke))
	for key, ts := range e.lastSpoke {
		producer, scope := splitRefKey(key)
		seen = append(seen, ProducerSeen{Producer: producer, Scope: scope, LastSeen: ts})
	}
	return seen
}
