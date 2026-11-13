package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/store"
)

type ChangeDigestInput struct {
	To     string       `json:"to" jsonschema:"RFC 3339 instant the window ends at, e.g. the start of an alert (required); the window is read backwards from it"`
	Window string       `json:"window" jsonschema:"Go duration of the window before to, from 15m to 1h inclusive (default 15m); use graph_diff for a longer span"`
	Scope  *DigestScope `json:"scope,omitempty" jsonschema:"restrict to one entity and its graph neighbors up to depth 2, including neighbors linked or unlinked during the window"`
	First  int          `json:"first,omitempty" jsonschema:"maximum number of groups returned (default 50, max 200); groups are ordered newest first"`
}

type DigestScope struct {
	Entity string `json:"entity" jsonschema:"handle of the center entity: identity fingerprint, inline identity type:key=value, or logical id"`
	Depth  int    `json:"depth,omitempty" jsonschema:"graph hops from the center entity, 0 to 2 (default 0)"`
}

type ChangeDigestOutput struct {
	Covered      DigestWindow  `json:"covered" jsonschema:"window actually read, as RFC 3339 from/to"`
	TotalChanges int           `json:"total_changes" jsonschema:"number of retained events after the scope filter"`
	GroupCount   int           `json:"group_count" jsonschema:"number of groups before the first cut"`
	Truncated    bool          `json:"truncated" jsonschema:"true when more groups existed than first"`
	Groups       []DigestGroup `json:"groups" jsonschema:"groups ordered newest first; never null"`
	Note         string        `json:"note" jsonschema:"fixed caveat about ordering and causality, plus any retention-horizon warning"`
	Graph        GraphMeta     `json:"graph" jsonschema:"graph metadata for the projection the digest was read from"`
}

type DigestWindow struct {
	From string `json:"from" jsonschema:"RFC 3339 start of the window"`
	To   string `json:"to" jsonschema:"RFC 3339 end of the window"`
}

type DigestGroup struct {
	ID           string          `json:"id" jsonschema:"stable id: hex of the first 8 bytes of sha256 over the group key and the window bounds"`
	ChangeType   string          `json:"change_type" jsonschema:"e.g. entity.deleted or relation.added"`
	Type         string          `json:"type" jsonschema:"entity or relation type the group is about"`
	ChangedKeys  []string        `json:"changed_keys" jsonschema:"sorted keys changed in this group; empty when none"`
	Scope        string          `json:"scope" jsonschema:"instrumentation scope that observed the changes"`
	DeleteSource string          `json:"delete_source" jsonschema:"producer, liveness_expiry, or cascade for deletions; empty otherwise"`
	Count        int             `json:"count" jsonschema:"exact number of events in the group"`
	FirstAt      string          `json:"first_at" jsonschema:"RFC 3339, oldest event time in the group"`
	LastAt       string          `json:"last_at" jsonschema:"RFC 3339, newest event time in the group"`
	Structural   bool            `json:"structural" jsonschema:"true for structural relations and for created/deleted/identity_changed/state_changed entities"`
	Examples     []DigestExample `json:"examples" jsonschema:"up to five examples, newest first"`
	LabelKey     string          `json:"label_key" jsonschema:"dotted key for rendering in any language, e.g. entity.deleted.liveness_expiry"`
	LabelParams  map[string]any  `json:"label_params" jsonschema:"parameters for the label key; count is always an int"`
	Label        string          `json:"label" jsonschema:"English sentence built from the key and params"`
}

type DigestExample struct {
	Fingerprint     string `json:"fingerprint,omitempty" jsonschema:"identity fingerprint of the entity; empty for relations"`
	Type            string `json:"type" jsonschema:"entity or relation type"`
	DisplayName     string `json:"display_name,omitempty" jsonschema:"display name; for relations '<from> -> <to>'"`
	Host            string `json:"host,omitempty" jsonschema:"name of the host the entity sits on (host.name, else host.id); absent when no single host is found or the example is itself a host"`
	HostFingerprint string `json:"host_fingerprint,omitempty" jsonschema:"identity_fingerprint of that host, the handle to open it; absent with host"`
	At              string `json:"at" jsonschema:"RFC 3339 event time"`
}

const digestNote = "Groups are ordered newest first and are not ranked by relevance. event_time is when a producer observed a change, not when it became true: changes closer together than the producer's polling interval carry no ordering, and no causal conclusion may be drawn from them."

func (s *Server) changeDigest(ctx context.Context, _ *mcpsdk.CallToolRequest, in ChangeDigestInput) (*mcpsdk.CallToolResult, ChangeDigestOutput, error) {
	if in.To == "" {
		return nil, ChangeDigestOutput{}, fmt.Errorf("to is required as an RFC 3339 instant")
	}
	to, err := time.Parse(time.RFC3339, in.To)
	if err != nil {
		return nil, ChangeDigestOutput{}, fmt.Errorf("to must be RFC 3339: %v", err)
	}
	window := in.Window
	if window == "" {
		window = "15m"
	}
	d, err := time.ParseDuration(window)
	if err != nil || d < 15*time.Minute || d > time.Hour {
		return nil, ChangeDigestOutput{}, fmt.Errorf("window must be between 15m and 1h: change_digest reads a short window before an instant; use graph_diff for a longer span")
	}
	from := to.Add(-d)
	first := in.First
	if first <= 0 {
		first = 50
	}
	if first > 200 {
		first = 200
	}

	// Heartbeats are excluded at the index so a live window is not drowned in them.
	var events []model.Event
	err = s.store.ScanTimeIndex(ctx, from, to.Add(time.Nanosecond), true, func(e store.TimeIndexEntry) error {
		if e.Tagged && e.ChangeType == model.EntityUnchanged {
			return nil
		}
		rev, ok, rerr := s.store.Resolve(e.Seq)
		if rerr != nil {
			return rerr
		}
		if !ok || eventChangeType(rev) == model.EntityUnchanged {
			return nil // a pre-tagging heartbeat is only recognizable once resolved
		}
		events = append(events, rev)
		return nil
	})
	if err != nil {
		return nil, ChangeDigestOutput{}, err
	}

	if in.Scope != nil {
		if in.Scope.Depth < 0 || in.Scope.Depth > 2 {
			return nil, ChangeDigestOutput{}, fmt.Errorf("scope depth must be between 0 and 2")
		}
		id, ok := s.graph.ResolveHandle(in.Scope.Entity)
		if ok {
			_, ok, _ = s.graph.GetEntity(id)
		}
		if !ok {
			return nil, ChangeDigestOutput{}, fmt.Errorf("scope entity not found: %s", in.Scope.Entity)
		}
		set := map[model.EntityID]bool{id: true}
		frontier := []model.EntityID{id}
		for depth := 0; depth < in.Scope.Depth; depth++ {
			var next []model.EntityID
			for _, cur := range frontier {
				edges := edgesOf(s.graph, cur, "")
				for i := range edges {
					if other := edges[i].other; !set[other] {
						set[other] = true
						next = append(next, other)
					}
				}
			}
			frontier = next
		}
		// One pass: a relation added or removed inside the window pulls its
		// other endpoint in, catching a neighbor that disappeared during the window.
		for _, ev := range events {
			if ev.Relation == nil {
				continue
			}
			ct := ev.Relation.ChangeType
			if ct != model.RelationAdded && ct != model.RelationRemoved {
				continue
			}
			fromID, toID := ev.Relation.Relation.From, ev.Relation.Relation.To
			if set[fromID] && !set[toID] {
				set[toID] = true
			}
			if set[toID] && !set[fromID] {
				set[fromID] = true
			}
		}
		filtered := make([]model.Event, 0, len(events))
		for _, ev := range events {
			if ev.Entity != nil {
				if set[ev.Entity.Entity.ID] {
					filtered = append(filtered, ev)
				}
			} else if ev.Relation != nil {
				if set[ev.Relation.Relation.From] || set[ev.Relation.Relation.To] {
					filtered = append(filtered, ev)
				}
			}
		}
		events = filtered
	}

	name := func(id model.EntityID) string {
		e, ok, _ := s.graph.GetEntity(id)
		if !ok {
			return ""
		}
		return model.DisplayName(e)
	}
	hr := newHostResolver(s.graph, events)
	groups, total, groupCount, truncated := groupEvents(events, from, to, first, name, hr.forEvent)
	note := digestNote
	if w := s.horizonWarning(from, to); w != "" {
		note = w + " " + note
	}
	out := ChangeDigestOutput{
		Covered:      DigestWindow{From: formatTime(from), To: formatTime(to)},
		TotalChanges: total,
		GroupCount:   groupCount,
		Truncated:    truncated,
		Groups:       groups,
		Note:         note,
		Graph:        s.graphMeta(s.graph, ""),
	}
	if out.Groups == nil {
		out.Groups = []DigestGroup{}
	}
	return nil, out, nil
}

func digestEventTime(ev model.Event) time.Time {
	if ev.Entity != nil {
		return ev.Entity.EventTime
	}
	if ev.Relation != nil {
		return ev.Relation.EventTime
	}
	return time.Time{}
}

func digestKey(ev model.Event) string {
	ct := eventChangeType(ev)
	var typ, scope, src string
	var keys []string
	if ev.Entity != nil {
		typ = ev.Entity.Entity.Type
		scope = ev.Entity.Scope
		src = string(ev.Entity.DeleteSource)
		keys = ev.Entity.ChangedKeys
	} else if ev.Relation != nil {
		typ = ev.Relation.Relation.Type
		scope = ev.Relation.Scope
		src = string(ev.Relation.DeleteSource)
		keys = ev.Relation.ChangedKeys
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	return ct.String() + "|" + typ + "|" + strings.Join(sorted, ",") + "|" + scope + "|" + src
}

func digestExample(ev model.Event, name func(model.EntityID) string) DigestExample {
	at := formatTime(digestEventTime(ev))
	if ev.Entity != nil {
		return DigestExample{
			Fingerprint: ev.Entity.Entity.IdentityHash(),
			Type:        ev.Entity.Entity.Type,
			DisplayName: model.DisplayName(ev.Entity.Entity),
			At:          at,
		}
	}
	rel := ev.Relation.Relation
	fromName := name(rel.From)
	if fromName == "" {
		fromName = "?"
	}
	toName := name(rel.To)
	if toName == "" {
		toName = "?"
	}
	display := ""
	if fromName != "?" || toName != "?" {
		display = fromName + " -> " + toName
	}
	return DigestExample{
		Type:        rel.Type,
		DisplayName: display,
		At:          at,
	}
}

type digestAcc struct {
	key           string
	changeType    string
	typ           string
	changedKeys   []string
	scope         string
	deleteSource  string
	structural    bool
	count         int
	firstAt       time.Time
	lastAt        time.Time
	examples      []DigestExample
	exampleTimes  []time.Time
	exampleEvents []model.Event
}

// addExample keeps at most the five newest examples, newest first; equal
// instants keep arrival order. The raw events are kept in parallel so the
// host is resolved only for the examples actually returned.
func (a *digestAcc) addExample(ex DigestExample, ev model.Event, at time.Time) {
	pos := len(a.exampleTimes)
	for i, t := range a.exampleTimes {
		if t.Before(at) {
			pos = i
			break
		}
	}
	a.examples = append(a.examples, DigestExample{})
	a.exampleTimes = append(a.exampleTimes, time.Time{})
	a.exampleEvents = append(a.exampleEvents, model.Event{})
	copy(a.examples[pos+1:], a.examples[pos:])
	copy(a.exampleTimes[pos+1:], a.exampleTimes[pos:])
	copy(a.exampleEvents[pos+1:], a.exampleEvents[pos:])
	a.examples[pos] = ex
	a.exampleTimes[pos] = at
	a.exampleEvents[pos] = ev
	if len(a.examples) > 5 {
		a.examples = a.examples[:5]
		a.exampleTimes = a.exampleTimes[:5]
		a.exampleEvents = a.exampleEvents[:5]
	}
}

func groupEvents(events []model.Event, from, to time.Time, first int, name func(model.EntityID) string, host func(model.Event) (string, string)) (groups []DigestGroup, total, groupCount int, truncated bool) {
	if first <= 0 {
		first = 50
	}
	if first > 200 {
		first = 200
	}
	accs := map[string]*digestAcc{}
	order := []string{}
	for _, ev := range events {
		key := digestKey(ev)
		acc, ok := accs[key]
		if !ok {
			ct := eventChangeType(ev)
			acc = &digestAcc{key: key, changeType: ct.String(), changedKeys: []string{}}
			if ev.Entity != nil {
				acc.typ = ev.Entity.Entity.Type
				acc.scope = ev.Entity.Scope
				acc.deleteSource = string(ev.Entity.DeleteSource)
				acc.changedKeys = append([]string(nil), ev.Entity.ChangedKeys...)
				acc.structural = ct == model.EntityCreated || ct == model.EntityDeleted ||
					ct == model.EntityIdentityChanged || ct == model.EntityStateChanged
			} else if ev.Relation != nil {
				acc.typ = ev.Relation.Relation.Type
				acc.scope = ev.Relation.Scope
				acc.deleteSource = string(ev.Relation.DeleteSource)
				acc.changedKeys = append([]string(nil), ev.Relation.ChangedKeys...)
				acc.structural = ev.Relation.Relation.Structural
			}
			sort.Strings(acc.changedKeys)
			accs[key] = acc
			order = append(order, key)
		}
		at := digestEventTime(ev)
		acc.count++
		if acc.count == 1 || at.Before(acc.firstAt) {
			acc.firstAt = at
		}
		if acc.count == 1 || at.After(acc.lastAt) {
			acc.lastAt = at
		}
		acc.addExample(digestExample(ev, name), ev, at)
	}
	// Sort on the accumulators' time.Time values, then build the groups in
	// that order so no RFC 3339 string is ever re-parsed.
	sorted := make([]*digestAcc, 0, len(order))
	ids := make(map[*digestAcc]string, len(order))
	for _, key := range order {
		acc := accs[key]
		sum := sha256.Sum256([]byte(acc.key + "|" + formatTime(from) + "|" + formatTime(to)))
		ids[acc] = hex.EncodeToString(sum[:8])
		sorted = append(sorted, acc)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].lastAt.Equal(sorted[j].lastAt) {
			return sorted[i].lastAt.After(sorted[j].lastAt)
		}
		return ids[sorted[i]] < ids[sorted[j]]
	})
	groups = make([]DigestGroup, 0, len(sorted))
	for _, acc := range sorted {
		groups = append(groups, DigestGroup{
			ID:           ids[acc],
			ChangeType:   acc.changeType,
			Type:         acc.typ,
			ChangedKeys:  acc.changedKeys,
			Scope:        acc.scope,
			DeleteSource: acc.deleteSource,
			Count:        acc.count,
			FirstAt:      formatTime(acc.firstAt),
			LastAt:       formatTime(acc.lastAt),
			Structural:   acc.structural,
			Examples:     acc.examples,
		})
	}
	for i := range groups {
		groups[i].LabelKey, groups[i].LabelParams, groups[i].Label = labelFor(groups[i])
	}
	groupCount = len(groups)
	truncated = groupCount > first
	if truncated {
		groups = groups[:first]
	}
	// Resolve the host only for the examples actually returned: resolution
	// is a graph walk, so doing it for every event would be wasteful.
	for i := range groups {
		acc := sorted[i]
		for j := range groups[i].Examples {
			groups[i].Examples[j].Host, groups[i].Examples[j].HostFingerprint = host(acc.exampleEvents[j])
		}
	}
	return groups, len(events), groupCount, truncated
}

func labelFor(g DigestGroup) (key string, params map[string]any, text string) {
	key = g.ChangeType
	if (g.ChangeType == "entity.deleted" || g.ChangeType == "relation.removed") && g.DeleteSource != "" {
		key += "." + g.DeleteSource
	}
	keys := g.ChangedKeys
	if keys == nil {
		keys = []string{}
	}
	params = map[string]any{
		"count":         g.Count,
		"type":          g.Type,
		"changed_keys":  keys,
		"scope":         g.Scope,
		"delete_source": g.DeleteSource,
		"first_at":      g.FirstAt,
		"last_at":       g.LastAt,
	}
	count := strconv.Itoa(g.Count)
	switch key {
	case "entity.created":
		text = count + " " + g.Type + " appeared"
	case "entity.deleted.producer":
		text = count + " " + g.Type + " reported gone by their producer"
	case "entity.deleted.liveness_expiry":
		text = count + " " + g.Type + " stopped reporting (liveness expired)"
	case "entity.deleted.cascade":
		text = count + " " + g.Type + " removed because something they depended on disappeared"
	case "entity.deleted":
		text = count + " " + g.Type + " disappeared"
	case "entity.attribute_updated", "entity.state_changed":
		changed := strings.Join(keys, ", ")
		if changed == "" {
			changed = "attributes"
		}
		text = count + " " + g.Type + " changed " + changed
	case "entity.identity_changed":
		text = count + " " + g.Type + " changed identity"
	case "relation.added":
		text = count + " " + g.Type + " relations added"
	case "relation.removed.liveness_expiry":
		text = count + " " + g.Type + " relations removed (liveness expired)"
	case "relation.removed.producer":
		text = count + " " + g.Type + " relations removed (producer)"
	case "relation.removed.cascade":
		text = count + " " + g.Type + " relations removed (cascade)"
	case "relation.removed":
		text = count + " " + g.Type + " relations removed"
	case "relation.attribute_changed":
		text = count + " " + g.Type + " relations changed"
	default:
		text = count + " " + g.Type + " " + g.ChangeType
	}
	first, ferr := time.Parse(time.RFC3339Nano, g.FirstAt)
	last, lerr := time.Parse(time.RFC3339Nano, g.LastAt)
	if ferr == nil && lerr == nil {
		if first.Equal(last) {
			text += " at " + first.UTC().Format("15:04:05") + " UTC"
		} else {
			text += " between " + first.UTC().Format("15:04:05") + " and " + last.UTC().Format("15:04:05") + " UTC"
		}
	}
	return key, params, text
}
