// Package demo builds the phase-1 demonstration fixture: "a day in the life of
// web-server-1", a 24-hour simulated evolution of one host's infrastructure.
// The scenario is applied through the change engine exactly as live OTLP
// ingestion would be, so it exercises the full pipeline (classification, event
// log, projection) and every change type the engine emits (eight of the nine
// taxonomy types, ADR 0006; identity_changed is retired under ADR 0018). It is the
// fixture behind the LLM example prompts in docs/demo/llm-prompts.md.
package demo

import (
	"fmt"
	"time"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
)

// Clock is a settable clock used to stamp recorded_at while seeding. It lets the
// fixture model facts recorded later than they became true (bi-temporal, ADR
// 0005): construct the engine with change.WithClock(clk.Now) and the scenario
// advances clk as it applies each step.
type Clock struct{ t time.Time }

// NewClock returns a Clock positioned at the zero time.
func NewClock() *Clock { return &Clock{} }

// Now returns the clock's current instant.
func (c *Clock) Now() time.Time { return c.t }

func (c *Clock) set(t time.Time) { c.t = t }

// Engine is the subset of *change.Engine the scenario drives.
type Engine interface {
	ObserveEntity(change.EntityObservation) (model.Event, error)
	ObserveRelation(change.RelationObservation) (model.Event, bool, error)
	RemoveRelation(change.RelationObservation) (model.Event, bool, error)
	DeleteEntity(change.EntityObservation) (model.Event, bool, error)
}

// Summary reports what Run produced.
type Summary struct {
	Start  time.Time
	End    time.Time
	Events int
}

// Run applies the scenario to eng, beginning at start and spanning 24 hours.
// clk must be the clock eng was constructed with. It returns a summary or the
// first error encountered.
func Run(eng Engine, clk *Clock, start time.Time) (Summary, error) {
	s := &seeder{eng: eng, clk: clk, start: start.UTC()}
	s.play()
	if s.err != nil {
		return Summary{}, s.err
	}
	return Summary{Start: s.start, End: s.at(24 * time.Hour), Events: s.events}, nil
}

const host1 = "web-server-1"

type seeder struct {
	eng    Engine
	clk    *Clock
	start  time.Time
	events int
	err    error
}

func (s *seeder) at(d time.Duration) time.Time { return s.start.Add(d) }

// --- typed-value helpers -----------------------------------------------------

func str(k, v string) model.KeyValue       { return model.KeyValue{Key: k, Value: model.StringValue(v)} }
func num(k string, v int64) model.KeyValue { return model.KeyValue{Key: k, Value: model.IntValue(v)} }
func boolean(k string, v bool) model.KeyValue {
	return model.KeyValue{Key: k, Value: model.BoolValue(v)}
}

func kvs(items ...model.KeyValue) []model.KeyValue { return items }

// --- identities --------------------------------------------------------------

func hostID() []model.KeyValue { return kvs(str("host.name", host1)) }

// process.creation.time values (OTel semconv) — stable for a process's lifetime,
// so process.pid + process.creation.time is an immutable identity even though a
// bare pid is reused.
const (
	nginxStarted    = "2026-06-01T00:00:00Z"
	dockerdStarted  = "2026-06-01T02:00:00Z"
	postgresStarted = "2026-06-01T09:00:00Z"
	nginxRestarted  = "2026-06-01T18:00:00Z"
)

// procID is the OTel-semconv process identity: process.pid + process.creation.time.
// The creation time disambiguates PID reuse, so the pair is immutable for the
// process's lifetime (ADR 0018): a real restart yields a new creation time — a
// genuinely new process entity (delete + create) — while a descriptive change
// (e.g. status) at the same pid + creation time is an attribute_updated. The
// executable name is a *descriptive* attribute.
func procID(pid int64, creation string) []model.KeyValue {
	return kvs(num("process.pid", pid), str("process.creation.time", creation))
}
func ifaceID(name string) []model.KeyValue {
	return kvs(str("host.name", host1), str("interface.name", name))
}
func addrID(addr string) []model.KeyValue { return kvs(str("network.address", addr)) }
func routeID(dst string) []model.KeyValue {
	return kvs(str("host.name", host1), str("network.route.destination", dst))
}
func listenerID(port int64) []model.KeyValue {
	// A single composite identity key: two listeners on the same host differ
	// only in their port. A single composite key is a clean immutable identity
	// (ADR 0018); under exact matching distinct ports are distinct entities.
	return kvs(str("service.endpoint", fmt.Sprintf("%s:%d", host1, port)))
}

// --- step helpers ------------------------------------------------------------

// observe applies an entity observation. event is the event-time offset from
// start; recorded is the recorded-at offset (use the same value unless modeling
// a late-recorded fact).
func (s *seeder) observe(event, recorded time.Duration, typ string, identity, attrs []model.KeyValue) {
	if s.err != nil {
		return
	}
	s.clk.set(s.at(recorded))
	if _, err := s.eng.ObserveEntity(change.EntityObservation{
		Type: typ, Identity: identity, Attributes: attrs, EventTime: s.at(event),
	}); err != nil {
		s.err = fmt.Errorf("observe %s at %s: %w", typ, event, err)
		return
	}
	s.events++
}

func (s *seeder) deleteEntity(event time.Duration, typ string, identity []model.KeyValue) {
	if s.err != nil {
		return
	}
	s.clk.set(s.at(event))
	if _, _, err := s.eng.DeleteEntity(change.EntityObservation{
		Type: typ, Identity: identity, EventTime: s.at(event),
	}); err != nil {
		s.err = fmt.Errorf("delete %s at %s: %w", typ, event, err)
		return
	}
	s.events++
}

func (s *seeder) relate(event time.Duration, relType string, from, to change.EndpointRef, attrs []model.KeyValue) {
	if s.err != nil {
		return
	}
	s.clk.set(s.at(event))
	if _, _, err := s.eng.ObserveRelation(change.RelationObservation{
		Type: relType, From: from, To: to, Attributes: attrs, EventTime: s.at(event),
	}); err != nil {
		s.err = fmt.Errorf("relate %s at %s: %w", relType, event, err)
		return
	}
	s.events++
}

func (s *seeder) unrelate(event time.Duration, relType string, from, to change.EndpointRef) {
	if s.err != nil {
		return
	}
	s.clk.set(s.at(event))
	if _, _, err := s.eng.RemoveRelation(change.RelationObservation{
		Type: relType, From: from, To: to, EventTime: s.at(event),
	}); err != nil {
		s.err = fmt.Errorf("unrelate %s at %s: %w", relType, event, err)
		return
	}
	s.events++
}

func ep(typ string, identity []model.KeyValue) change.EndpointRef {
	return change.EndpointRef{Type: typ, Identity: identity}
}

// --- the scenario ------------------------------------------------------------

const (
	h  = time.Hour
	mn = time.Minute
)

func (s *seeder) play() {
	// 1. Discovery (t+0): the host and its initial topology come into view.
	s.observe(0, 0, model.TypeHost, hostID(), kvs(str("os.type", "linux"), str("os.description", "Ubuntu 24.04")))
	s.observe(0, 0, model.TypeProcess, procID(1001, nginxStarted), kvs(str("process.executable.name", "nginx"), str("status", "running")))
	s.relate(0, model.RelRunsOn, ep(model.TypeProcess, procID(1001, nginxStarted)), ep(model.TypeHost, hostID()), nil)

	s.observe(0, 0, model.TypeNetworkInterface, ifaceID("eth0"), kvs(str("oper_state", "up"), str("mac.address", "02:42:ac:11:00:05")))
	s.relate(0, model.RelHasInterface, ep(model.TypeHost, hostID()), ep(model.TypeNetworkInterface, ifaceID("eth0")), nil)

	s.observe(0, 0, model.TypeNetworkAddress, addrID("10.0.1.5"), kvs(num("prefix.length", 24)))
	s.relate(0, model.RelBoundTo, ep(model.TypeNetworkAddress, addrID("10.0.1.5")), ep(model.TypeNetworkInterface, ifaceID("eth0")), kvs(boolean("preferred", true)))

	s.observe(0, 0, model.TypeNetworkAddress, addrID("10.0.1.1"), kvs(str("role", "gateway")))
	s.observe(0, 0, model.TypeNetworkRoute, routeID("0.0.0.0/0"), kvs(str("next_hop", "10.0.1.1")))
	s.relate(0, model.RelNextHopVia, ep(model.TypeNetworkRoute, routeID("0.0.0.0/0")), ep(model.TypeNetworkAddress, addrID("10.0.1.1")), nil)

	s.observe(0, 0, model.TypeServiceListener, listenerID(80), kvs(str("process.executable.name", "nginx"), str("transport", "tcp")))
	s.relate(0, model.RelListensOn, ep(model.TypeServiceListener, listenerID(80)), ep(model.TypeNetworkInterface, ifaceID("eth0")), nil)

	// 2. New container daemon (t+2h): dockerd starts.
	s.observe(2*h, 2*h, model.TypeProcess, procID(2002, dockerdStarted), kvs(str("process.executable.name", "dockerd"), str("status", "running")))
	s.relate(2*h, model.RelRunsOn, ep(model.TypeProcess, procID(2002, dockerdStarted)), ep(model.TypeHost, hostID()), nil)

	// 3. eth0 goes down (t+6h) — but this fact is only RECORDED 20 minutes later
	//    (a late-arriving observation), so an asKnownAt audit before t+6h20 still
	//    sees eth0 up. state_changed on oper_state.
	s.observe(6*h, 6*h+20*mn, model.TypeNetworkInterface, ifaceID("eth0"), kvs(str("oper_state", "down"), str("mac.address", "02:42:ac:11:00:05")))

	// 4. eth0 comes back on a new subnet (t+6h30): interface up, old address
	//    gone, new address bound.
	s.observe(6*h+30*mn, 6*h+30*mn, model.TypeNetworkInterface, ifaceID("eth0"), kvs(str("oper_state", "up"), str("mac.address", "02:42:ac:11:00:05")))
	s.unrelate(6*h+30*mn, model.RelBoundTo, ep(model.TypeNetworkAddress, addrID("10.0.1.5")), ep(model.TypeNetworkInterface, ifaceID("eth0")))
	s.deleteEntity(6*h+30*mn, model.TypeNetworkAddress, addrID("10.0.1.5"))
	s.observe(6*h+30*mn, 6*h+30*mn, model.TypeNetworkAddress, addrID("10.0.2.7"), kvs(num("prefix.length", 24)))
	s.relate(6*h+30*mn, model.RelBoundTo, ep(model.TypeNetworkAddress, addrID("10.0.2.7")), ep(model.TypeNetworkInterface, ifaceID("eth0")), kvs(boolean("preferred", true)))

	// 5. postgres starts (t+9h) and listens on :5432.
	s.observe(9*h, 9*h, model.TypeProcess, procID(3003, postgresStarted), kvs(str("process.executable.name", "postgres"), str("status", "running")))
	s.relate(9*h, model.RelRunsOn, ep(model.TypeProcess, procID(3003, postgresStarted)), ep(model.TypeHost, hostID()), nil)
	s.observe(9*h, 9*h, model.TypeServiceListener, listenerID(5432), kvs(str("process.executable.name", "postgres"), str("transport", "tcp")))
	s.relate(9*h, model.RelListensOn, ep(model.TypeServiceListener, listenerID(5432)), ep(model.TypeNetworkInterface, ifaceID("eth0")), nil)

	// 6. Default gateway changes (t+12h): new gateway address, the route's
	//    next_hop attribute updates and its next_hop_via edge moves, the old
	//    gateway is removed, and the new address is marked no-longer-preferred
	//    (a relation.attribute_changed on bound_to).
	s.observe(12*h, 12*h, model.TypeNetworkAddress, addrID("10.0.2.1"), kvs(str("role", "gateway")))
	s.observe(12*h, 12*h, model.TypeNetworkRoute, routeID("0.0.0.0/0"), kvs(str("next_hop", "10.0.2.1")))
	s.unrelate(12*h, model.RelNextHopVia, ep(model.TypeNetworkRoute, routeID("0.0.0.0/0")), ep(model.TypeNetworkAddress, addrID("10.0.1.1")))
	s.relate(12*h, model.RelNextHopVia, ep(model.TypeNetworkRoute, routeID("0.0.0.0/0")), ep(model.TypeNetworkAddress, addrID("10.0.2.1")), nil)
	s.deleteEntity(12*h, model.TypeNetworkAddress, addrID("10.0.1.1"))
	s.relate(12*h, model.RelBoundTo, ep(model.TypeNetworkAddress, addrID("10.0.2.7")), ep(model.TypeNetworkInterface, ifaceID("eth0")), kvs(boolean("preferred", false)))

	// 7a. nginx config reload (t+15h): the SAME process (pid + creation time
	//     unchanged) flips a descriptive attribute, status running -> reloading
	//     -> running — an entity.attribute_updated, not an identity change.
	s.observe(15*h, 15*h, model.TypeProcess, procID(1001, nginxStarted), kvs(str("process.executable.name", "nginx"), str("status", "reloading")))
	s.observe(15*h+10*mn, 15*h+10*mn, model.TypeProcess, procID(1001, nginxStarted), kvs(str("process.executable.name", "nginx"), str("status", "running")))

	// 7b. nginx restarts (t+18h): a real restart yields a new pid AND a new
	//     creation time, so per the OTel semconv identity (process.pid +
	//     process.creation.time) it is a genuinely new process — delete the old,
	//     create the new, and move the runs_on edge (ADR 0018).
	s.unrelate(18*h, model.RelRunsOn, ep(model.TypeProcess, procID(1001, nginxStarted)), ep(model.TypeHost, hostID()))
	s.deleteEntity(18*h, model.TypeProcess, procID(1001, nginxStarted))
	s.observe(18*h, 18*h, model.TypeProcess, procID(1010, nginxRestarted), kvs(str("process.executable.name", "nginx"), str("status", "running")))
	s.relate(18*h, model.RelRunsOn, ep(model.TypeProcess, procID(1010, nginxRestarted)), ep(model.TypeHost, hostID()), nil)

	// 8. The container crashes (t+22h): dockerd disappears and stops running on
	//    the host. A host heartbeat confirms the host is otherwise unchanged.
	s.unrelate(22*h, model.RelRunsOn, ep(model.TypeProcess, procID(2002, dockerdStarted)), ep(model.TypeHost, hostID()))
	s.deleteEntity(22*h, model.TypeProcess, procID(2002, dockerdStarted))
	s.observe(22*h, 22*h, model.TypeHost, hostID(), kvs(str("os.type", "linux"), str("os.description", "Ubuntu 24.04")))
}
