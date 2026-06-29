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
	ID         string      `json:"id" jsonschema:"the stable logical entity id (a ULID), stable across identity changes"`
	Type       string      `json:"type" jsonschema:"the entity type, e.g. host, process, network_interface"`
	Label      string      `json:"label" jsonschema:"a short human-readable label derived from the identifying attributes"`
	Identity   []Attribute `json:"identity,omitempty" jsonschema:"the identifying attributes that together identify this entity; omitted in compact verbosity"`
	Attributes []Attribute `json:"attributes,omitempty" jsonschema:"descriptive, non-identifying attributes; omitted in compact verbosity"`
	Deleted    bool        `json:"deleted" jsonschema:"true if the entity has been observed deleted"`
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
	DeleteReason string    `json:"delete_reason,omitempty" jsonschema:"why an entity was deleted, e.g. terminated/expired/evicted; open enum, may be empty"`
	Entity       *Entity   `json:"entity,omitempty"`
	Relation     *Relation `json:"relation,omitempty"`
}

func valueString(v model.Value) (str, typ string) {
	switch v.Kind() {
	case model.KindInt:
		return v.Display(), "int"
	case model.KindDouble:
		return v.Display(), "double"
	case model.KindBool:
		return v.Display(), "bool"
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

// label builds a compact, human-readable identifier from the entity's
// identifying attributes, e.g. "host hostname=web-server-1".
func label(e model.Entity) string {
	var b strings.Builder
	b.WriteString(e.Type)
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
		ID:      string(e.ID),
		Type:    e.Type,
		Label:   label(e),
		Deleted: deleted,
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
