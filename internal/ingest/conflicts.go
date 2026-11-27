package ingest

import (
	"slices"
	"sync"
	"time"
)

const conflictWindow = 5 * time.Minute

const conflictReasonSharedInstanceID = "shared_instance_id"

const maxTrackedProducers = 10000

const maxHostsPerProducer = 64

// conflictDetector flags multiple host.id values reporting under a single
// producer identity, which typically means two machines share one agent
// identity. State is bounded so a flood of distinct producers cannot grow
// memory without limit.
type conflictDetector struct {
	mu        sync.Mutex
	now       func() time.Time
	producers map[string]*producerHosts
}

type producerHosts struct {
	seen     map[string]time.Time // hostID -> last seen
	lastWarn time.Time
}

func newConflictDetector() *conflictDetector {
	return &conflictDetector{
		now:       time.Now,
		producers: make(map[string]*producerHosts),
	}
}

func (d *conflictDetector) observe(producer, hostID string) (conflicting, warn bool) {
	if producer == "" || hostID == "" {
		return false, false
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.now()

	ph, ok := d.producers[producer]
	if !ok {
		if len(d.producers) >= maxTrackedProducers {
			d.sweepLocked(now)
			if len(d.producers) >= maxTrackedProducers {
				return false, false
			}
		}
		ph = &producerHosts{seen: make(map[string]time.Time)}
		d.producers[producer] = ph
	}

	if _, known := ph.seen[hostID]; known || len(ph.seen) < maxHostsPerProducer {
		ph.seen[hostID] = now
	}
	d.pruneProducerLocked(ph, now)

	live := 0
	for _, t := range ph.seen {
		if now.Sub(t) < conflictWindow {
			live++
		}
	}
	if live < 2 {
		return false, false
	}

	if ph.lastWarn.IsZero() || now.Sub(ph.lastWarn) >= conflictWindow {
		ph.lastWarn = now
		return true, true
	}
	return true, false
}

func (d *conflictDetector) ConflictingHosts(producer string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	ph, ok := d.producers[producer]
	if !ok {
		return nil
	}

	now := d.now()
	var hosts []string
	for hostID, t := range ph.seen {
		if now.Sub(t) < conflictWindow {
			hosts = append(hosts, hostID)
		}
	}
	if len(hosts) < 2 {
		return nil
	}
	slices.Sort(hosts)
	return hosts
}

func (d *conflictDetector) pruneProducerLocked(ph *producerHosts, now time.Time) {
	for hostID, t := range ph.seen {
		if now.Sub(t) >= conflictWindow {
			delete(ph.seen, hostID)
		}
	}
}

func (d *conflictDetector) sweepLocked(now time.Time) {
	for producer, ph := range d.producers {
		for hostID, t := range ph.seen {
			if now.Sub(t) >= conflictWindow {
				delete(ph.seen, hostID)
			}
		}
		if len(ph.seen) == 0 {
			delete(d.producers, producer)
		}
	}
}
