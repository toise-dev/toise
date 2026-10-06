package mcp

import (
	"fmt"
	"strings"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

// Attribute is a single typed attribute rendered for an LLM: the key, its value
// as a string, and the value's type so the model knows how to read it.
type Attribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Type  string `json:"type" jsonschema:"the value's type: string, int, double, or bool"`
}

// Entity is an entity rendered for an LLM. Label is a short human-readable
// summary derived from the identifying attributes so the model can refer to the
// entity without re-deriving it.
type Entity struct {
	ID                  string      `json:"id" jsonschema:"this replica's local id for the entity (a ULID); it differs between replicas and is re-minted after a long silence, so prefer identity_fingerprint to carry between calls"`
	IdentityFingerprint string      `json:"identity_fingerprint" jsonschema:"the handle that names the entity itself, derived from its identifying attributes; every replica computes the same one, and any tool taking an entity id takes this instead"`
	Type                string      `json:"type" jsonschema:"the entity type, e.g. host, process, network_interface"`
	DisplayName         string      `json:"display_name,omitempty" jsonschema:"what a human calls this entity - dash172 for a host, senhub-ping for a container - or absent when nothing observed on it reads better than its identity; render it as-is, and never group, join or match on it: a display name drifts when a thing is renamed, identity_fingerprint does not"`
	Label               string      `json:"label" jsonschema:"a one-line summary for scanning: the type, the display name, then the identifying attributes; not a key and not meant to be shown on its own - use display_name to render and identity_fingerprint to match"`
	Identity            []Attribute `json:"identity,omitempty" jsonschema:"the identifying attributes that together identify this entity; omitted in compact verbosity"`
	Attributes          []Attribute `json:"attributes,omitempty" jsonschema:"descriptive, non-identifying attributes; omitted in compact verbosity"`
	Deleted             bool        `json:"deleted" jsonschema:"true if the entity has been observed deleted"`
}

// Relation is a typed directed edge rendered for an LLM.
type Relation struct {
	ID         string      `json:"id"`
	Type       string      `json:"type" jsonschema:"the relation type, e.g. runs_on, connected_to"`
	FromID     string      `json:"from_id" jsonschema:"logical id of the source entity"`
	ToID       string      `json:"to_id" jsonschema:"logical id of the target entity"`
	Structural bool        `json:"structural" jsonschema:"true if appearance/disappearance of this edge is significant (alert-worthy)"`
	Attributes []Attribute `json:"attributes"`
}

// Change is one classified event rendered for an LLM. It is bi-temporal:
// EventTime is when the fact became true in the world, RecordedAt is when Toise
// learned it (ADR 0005). Exactly one of Entity or Relation is set.
type Change struct {
	EventID     string   `json:"event_id"`
	ChangeType  string   `json:"change_type" jsonschema:"the taxonomy name, e.g. entity.created, relation.added"`
	EventTime   string   `json:"event_time" jsonschema:"RFC 3339; when the change became true in the real world"`
	RecordedAt  string   `json:"recorded_at" jsonschema:"RFC 3339; when Toise recorded the change"`
	ChangedKeys []string `json:"changed_keys" jsonschema:"the attribute keys that changed, for update/state-change events"`
	// DeleteReason is the producer's open-enum motive on an entity.delete (e.g.
	// terminated, expired, evicted); empty when none was given or for non-delete
	// changes. Omitted from the payload when empty.
	DeleteReason string `json:"delete_reason,omitempty" jsonschema:"why an entity was deleted, e.g. terminated/expired/evicted; open enum, may be empty"`
	// DeleteSource attributes the AUTHOR of a disappearance: producer (explicit
	// delete or removal-by-absence), liveness_expiry (Toise's backstop reaped it
	// after a missed heartbeat), or cascade (an endpoint died and took the edge).
	// Distinct from DeleteReason, which is producer-supplied verbatim. Empty =
	// unknown (event predates provenance), never to be read as producer.
	DeleteSource string `json:"delete_source,omitempty" jsonschema:"who authored the disappearance: producer, liveness_expiry (missed heartbeat) or cascade (endpoint died); empty on older events"`
	// Disappearance is DeleteSource written out as a sentence. A bare enum next
	// to a field named like a cause invites the one reading no consumer should
	// make — that a human deleted something — so the payload carries the
	// meaning, not just the code (#346).
	Disappearance string    `json:"disappearance,omitempty" jsonschema:"delete_source in plain language, including what it does NOT mean"`
	Entity        *Entity   `json:"entity,omitempty"`
	Relation      *Relation `json:"relation,omitempty"`
}

// disappearanceGloss renders a delete_source as a sentence a reader cannot
// misread. Every gloss states the negative explicitly: none of these sources
// means an operator deleted anything, and consumers have concluded exactly that
// from the bare enum — three invented human operations, asserted at high
// confidence, in one incident (#346).
func disappearanceGloss(src model.DeleteSource) string {
	switch src {
	case model.DeleteSourceProducer:
		return "the producer reported this gone — it either sent an explicit delete or stopped listing the entity it had been reporting. " +
			"This is an observation about the world, NOT an operator action: nothing here says a human removed anything."
	case model.DeleteSourceLivenessExpiry:
		return "no news — the producer went silent past the interval it declared, so Toise expired the entity. " +
			"The thing may well still be running: this describes the observation ending, NOT the resource ending."
	case model.DeleteSourceCascade:
		return "collateral — an entity this edge touched disappeared, so the edge went with it. " +
			"Look at the entity that died for the real event; this row is a consequence, not a cause."
	default:
		return ""
	}
}

func valueString(v model.Value) (str, typ string) {
	switch v.Kind() {
	case model.KindInt:
		return v.Display(), "int"
	case model.KindDouble:
		return v.Display(), "double"
	case model.KindBool:
		return v.Display(), "bool"
	case model.KindArray:
		return v.Display(), "array"
	case model.KindKvlist:
		return v.Display(), "kvlist"
	default:
		return v.Display(), "string"
	}
}

func attrsOut(kvs []model.KeyValue) []Attribute {
	out := make([]Attribute, len(kvs))
	for i, kv := range kvs {
		val, typ := valueString(kv.Value)
		out[i] = Attribute{Key: kv.Key, Value: val, Type: typ}
	}
	return out
}

// label builds a compact, human-readable identifier: the type, the entity's
// display name when it has one, then its identifying attributes — e.g.
// "host dash172 host.id=6ccc0dcc-...". The name comes first because this is what
// a caller scanning many entities reads, and an identity is often a UUID or a
// digest; the identity stays in full because the name is not a key (#378).
func label(e model.Entity) string {
	var b strings.Builder
	b.WriteString(e.Type)
	if name := model.LabelName(e); name != "" {
		b.WriteByte(' ')
		b.WriteString(name)
	}
	for _, kv := range e.Identity {
		val, _ := valueString(kv.Value)
		b.WriteByte(' ')
		b.WriteString(kv.Key)
		b.WriteByte('=')
		b.WriteString(val)
	}
	return b.String()
}

func entityOut(e model.Entity, deleted bool) Entity {
	return entityOutV(e, deleted, false)
}

// entityOutV renders an entity at the requested verbosity. Compact drops the
// identity and descriptive attribute lists, keeping the id, type, label and
// deleted flag — the slim shape an LLM asks for to scan many entities cheaply,
// then re-fetches one in full. Full is the default and unchanged.
func entityOutV(e model.Entity, deleted, compact bool) Entity {
	out := Entity{
		ID:                  string(e.ID),
		IdentityFingerprint: e.IdentityHash(),
		Type:                e.Type,
		DisplayName:         model.DisplayName(e),
		Label:               label(e),
		Deleted:             deleted,
	}
	if !compact {
		out.Identity = attrsOut(e.Identity)
		out.Attributes = attrsOut(e.Attributes)
	}
	return out
}

// verbosity values for the read tools' optional `verbosity` input.
const (
	verbosityFull    = "full"
	verbosityCompact = "compact"
)

// parseVerbosity reads the optional verbosity input. Empty defaults to full
// (backward-compatible); an unknown value is a friendly error.
func parseVerbosity(v string) (compact bool, err error) {
	switch v {
	case "", verbosityFull:
		return false, nil
	case verbosityCompact:
		return true, nil
	default:
		return false, fmt.Errorf("unknown verbosity %q: use %q or %q", v, verbosityFull, verbosityCompact)
	}
}

func relationOut(r model.Relation) Relation {
	return Relation{
		ID:         string(r.ID),
		Type:       r.Type,
		FromID:     string(r.From),
		ToID:       string(r.To),
		Structural: r.Structural,
		Attributes: attrsOut(r.Attributes),
	}
}

func changeOut(ev model.Event) Change {
	c := Change{ChangedKeys: []string{}}
	switch {
	case ev.Entity != nil:
		ee := ev.Entity
		c.EventID = ee.EventID
		c.ChangeType = ee.ChangeType.String()
		c.EventTime = formatTime(ee.EventTime)
		c.RecordedAt = formatTime(ee.RecordedAt)
		if ee.ChangedKeys != nil {
			c.ChangedKeys = ee.ChangedKeys
		}
		c.DeleteReason = ee.DeleteReason
		c.DeleteSource = string(ee.DeleteSource)
		c.Disappearance = disappearanceGloss(ee.DeleteSource)
		ent := entityOut(ee.Entity, ee.ChangeType == model.EntityDeleted)
		c.Entity = &ent
	case ev.Relation != nil:
		re := ev.Relation
		c.EventID = re.EventID
		c.ChangeType = re.ChangeType.String()
		c.EventTime = formatTime(re.EventTime)
		c.RecordedAt = formatTime(re.RecordedAt)
		if re.ChangedKeys != nil {
			c.ChangedKeys = re.ChangedKeys
		}
		c.DeleteSource = string(re.DeleteSource)
		c.Disappearance = disappearanceGloss(re.DeleteSource)
		rel := relationOut(re.Relation)
		c.Relation = &rel
	}
	return c
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// matches reports whether every wanted key/value is present (as a string-equal
// attribute) in the entity's identity or attributes. The shared predicate lives
// on the model so the GraphQL entities query filters identically.
func matches(e model.Entity, want map[string]string) bool {
	return e.MatchAll(want)
}

// GraphMeta is the provenance block every read answer carries: what the
// answering graph holds, how fresh its log is, and how far back it can answer.
// Three operator agents independently asked for the same thing before any of
// them would trust an answer (#346): a consumer's job is arbitrating between
// disagreeing sources, and a source that does not state its own scope and
// freshness cannot enter the arbitration — it gets hand-verified once, then
// bypassed. Absence is not evidence of absence, and this block is where an
// answer says how much absence it can even speak for.
type GraphMeta struct {
	Entities  int `json:"entities" jsonschema:"entities the answering graph holds"`
	Relations int `json:"relations" jsonschema:"relations the answering graph holds"`
	// NewestEvent dates the log, not the projection: a live graph with a stale
	// log means producers stopped talking, which is itself the finding.
	NewestEvent string `json:"newest_event,omitempty" jsonschema:"RFC 3339 event time of the newest event in this tenant's log; how fresh this instance is"`
	// OldestAnswerable is the prune horizon: an as_of read before it is refused
	// rather than answered wrongly.
	OldestAnswerable string `json:"oldest_answerable,omitempty" jsonschema:"RFC 3339 prune horizon; history and as_of reads reach no further back"`
	// AsOf is set when this answer reads a past instant rather than now.
	AsOf string `json:"as_of,omitempty" jsonschema:"set when this answer describes the graph as of a past instant, not the present"`
}

// graphMeta assembles the provenance block from the graph that actually
// answered (live or an as-of fold) and the tenant's log.
func (s *Server) graphMeta(g Graph, asOf string) GraphMeta {
	m := GraphMeta{Entities: g.EntityCount(), Relations: g.RelationCount(), AsOf: asOf}
	if t, ok, err := s.store.NewestEventTime(); err == nil && ok {
		m.NewestEvent = formatTime(t)
	}
	if h := s.store.PruneHorizon(); !h.IsZero() {
		m.OldestAnswerable = formatTime(h)
	}
	return m
}

// Resolution states how finely an answer's timestamps may be read. Toise's
// event_time is when a PRODUCER OBSERVED a fact, not when the fact became true,
// so the truth lies somewhere in the interval before it. Two changes closer
// together than the interval carry no ordering information at all.
//
// It is carried as a sentence and not only as a number for the same reason
// delete_source is (#346): a bare duration next to nanosecond timestamps invites
// the reading it exists to prevent. A real incident review concluded "the
// service returned 47 s BEFORE the address moved, so the two are unrelated" from
// two event_times 47 s apart, under a 30 s cadence — the gap was shorter than
// the resolution and meant nothing.
// noLivenessPromise is the Meaning for an entity no producer promised to
// refresh. It is one sentence doing the job delete_source's gloss does: a
// consumer that cannot ask a follow-up question needs the consequence spelled
// out, not a missing field to interpret.
const noLivenessPromise = "no producer declared a refresh interval for this entity, so Toise has no liveness promise to hold it to: its presence here means it was observed at least once and never explicitly deleted, and it will NOT expire on its own however long its producers stay silent. Read it as \"something asserted this and nothing has contradicted it\", not as \"a producer vouched for this recently\". The timestamps carry no resolution bound either, so no ordering or causal conclusion may be drawn from how close two of them are. Toise does not invent a default interval: that would be a promise no producer made."

type Resolution struct {
	ObservationInterval string `json:"observation_interval" jsonschema:"the liveness interval this entity's producers declared, e.g. 30s — an UPPER BOUND on the uncertainty, deliberately padded above the real reporting cadence, never below it; the literal \"none\" means NO producer promised a refresh, so the entity never expires on its own and its presence is not evidence of recency"`
	Meaning             string `json:"meaning" jsonschema:"what that implies for reading the timestamps in this answer, including what they cannot tell you"`
}

// resolutionFor renders the resolution block for an entity. When no producer
// declares an interval it says so instead of returning nothing (#363): an
// entity nobody promised to refresh never expires, and a reader cannot tell it
// apart from one asserted a minute ago unless the answer admits it. Saying
// nothing was the earlier behavior and it was the defect, not the honesty — the
// honesty is refusing to invent a bound, which this still does.
func (s *Server) resolutionFor(id model.EntityID) *Resolution {
	if s.cadence == nil {
		return nil
	}
	interval, ok := s.cadence.ObservationInterval(id)
	if !ok {
		return &Resolution{ObservationInterval: "none", Meaning: noLivenessPromise}
	}
	return &Resolution{
		ObservationInterval: interval.String(),
		Meaning: "every event_time here is when a producer OBSERVED the fact, not when it became true. This is the liveness interval its producers DECLARED, which is padded above their real reporting cadence — so it is an upper bound on the uncertainty, never an under-statement: the change happened somewhere within " +
			interval.String() + " before its event_time, often much closer. Two changes less than " + interval.String() +
			" apart therefore carry no guaranteed ordering, and a causal conclusion drawn from a gap that small — including against an external clock — needs evidence from outside Toise. " +
			"This is the coarsest declared interval among the producers referencing this entity right now; it is not recoverable for past observations.",
	}
}
