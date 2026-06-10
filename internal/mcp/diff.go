package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toise-dev/toise/internal/model"
)

// --- graph_diff ---

// GraphDiffInput selects the two instants to diff between. Give either a
// window (shorthand for "the last X") or an explicit from (with an optional
// to, defaulting to now).
type GraphDiffInput struct {
	Window string `json:"window,omitempty" jsonschema:"a positive Go duration looking back from now, e.g. 1h or 24h (shorthand for from=now-window)"`
	From   string `json:"from,omitempty" jsonschema:"RFC 3339 start instant (use either window or from, not both)"`
	To     string `json:"to,omitempty" jsonschema:"RFC 3339 end instant (default now; only valid with from)"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum items per bucket (default 50, max 200); totals always cover everything"`
}

// EntityDiff is a changed entity with what changed.
type EntityDiff struct {
	Entity
	ChangedKeys  []string `json:"changed_keys" jsonschema:"union of the attribute keys that changed in the window"`
	StateChanged bool     `json:"state_changed" jsonschema:"true if a state-flagged attribute changed"`
}

// DiffTotals counts every bucket before the per-bucket limit.
type DiffTotals struct {
	EntitiesCreated    int `json:"entities_created"`
	EntitiesDeleted    int `json:"entities_deleted"`
	EntitiesChanged    int `json:"entities_changed"`
	EntitiesTransient  int `json:"entities_transient"`
	RelationsAdded     int `json:"relations_added"`
	RelationsRemoved   int `json:"relations_removed"`
	RelationsChanged   int `json:"relations_changed"`
	RelationsTransient int `json:"relations_transient"`
}

// GraphDiffOutput is the folded net difference between the two instants.
type GraphDiffOutput struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Summary string `json:"summary" jsonschema:"a one-line natural-language summary of the net difference"`

	EntitiesCreated   []Entity     `json:"entities_created,omitempty" jsonschema:"entities present at to but not at from"`
	EntitiesDeleted   []Entity     `json:"entities_deleted,omitempty" jsonschema:"entities present at from but not at to"`
	EntitiesChanged   []EntityDiff `json:"entities_changed,omitempty" jsonschema:"entities present at both instants whose attributes changed"`
	EntitiesTransient []Entity     `json:"entities_transient,omitempty" jsonschema:"entities that appeared AND disappeared within the window (flapping)"`

	RelationsAdded     []Relation `json:"relations_added,omitempty"`
	RelationsRemoved   []Relation `json:"relations_removed,omitempty"`
	RelationsChanged   []Relation `json:"relations_changed,omitempty"`
	RelationsTransient []Relation `json:"relations_transient,omitempty" jsonschema:"relations that appeared AND disappeared within the window (flapping)"`

	Totals    DiffTotals `json:"totals"`
	Truncated bool       `json:"truncated" jsonschema:"true if any bucket was cut to the limit; totals still cover everything"`
}

// entityFold accumulates one entity's events across the window.
type entityFold struct {
	createdFirst  bool             // the first event in the window was entity.created
	lastLifecycle model.ChangeType // last created/deleted seen (0 = none)
	changedKeys   map[string]struct{}
	stateChanged  bool
	ent           model.Entity // latest payload
	seq           int          // arrival order, for stable output
}

// relationFold accumulates one relation's events across the window.
type relationFold struct {
	addedFirst    bool
	lastLifecycle model.ChangeType
	attrChanged   bool
	rel           model.Relation
	seq           int
}

func (s *Server) graphDiff(ctx context.Context, _ *mcpsdk.CallToolRequest, in GraphDiffInput) (*mcpsdk.CallToolResult, GraphDiffOutput, error) {
	from, to, err := s.diffBounds(in)
	if err != nil {
		return nil, GraphDiffOutput{}, err
	}
	limit := clampLimit(in.Limit)
	evs, err := s.store.ReadByTimeRange(ctx, from, to)
	if err != nil {
		return nil, GraphDiffOutput{}, fmt.Errorf("reading changes: %w", err)
	}

	entities := make(map[model.EntityID]*entityFold)
	relations := make(map[model.RelationID]*relationFold)
	for _, ev := range evs {
		switch {
		case ev.Entity != nil:
			ee := ev.Entity
			f := entities[ee.Entity.ID]
			if f == nil {
				f = &entityFold{
					createdFirst: ee.ChangeType == model.EntityCreated,
					changedKeys:  make(map[string]struct{}),
					seq:          len(entities),
				}
				entities[ee.Entity.ID] = f
			}
			f.ent = ee.Entity
			switch ee.ChangeType {
			case model.EntityCreated, model.EntityDeleted:
				f.lastLifecycle = ee.ChangeType
			case model.EntityAttributeUpdated, model.EntityStateChanged:
				for _, k := range ee.ChangedKeys {
					f.changedKeys[k] = struct{}{}
				}
				if ee.ChangeType == model.EntityStateChanged {
					f.stateChanged = true
				}
			}
		case ev.Relation != nil:
			re := ev.Relation
			f := relations[re.Relation.ID]
			if f == nil {
				f = &relationFold{
					addedFirst: re.ChangeType == model.RelationAdded,
					seq:        len(relations),
				}
				relations[re.Relation.ID] = f
			}
			f.rel = re.Relation
			switch re.ChangeType {
			case model.RelationAdded, model.RelationRemoved:
				f.lastLifecycle = re.ChangeType
			case model.RelationAttributeChanged:
				f.attrChanged = true
			}
		}
	}

	out := GraphDiffOutput{From: formatTime(from), To: formatTime(to)}
	type entityBucket struct {
		folds []*entityFold
		total *int
	}
	created := entityBucket{total: &out.Totals.EntitiesCreated}
	deleted := entityBucket{total: &out.Totals.EntitiesDeleted}
	changed := entityBucket{total: &out.Totals.EntitiesChanged}
	transient := entityBucket{total: &out.Totals.EntitiesTransient}
	for _, f := range entities {
		presentAtEnd := f.lastLifecycle != model.EntityDeleted
		switch {
		case f.createdFirst && presentAtEnd:
			created.folds = append(created.folds, f)
		case f.createdFirst && !presentAtEnd:
			transient.folds = append(transient.folds, f)
		case !presentAtEnd:
			deleted.folds = append(deleted.folds, f)
		case len(f.changedKeys) > 0:
			changed.folds = append(changed.folds, f)
			// heartbeat-only folds are dropped: no net difference.
		}
	}
	for _, b := range []*entityBucket{&created, &deleted, &changed, &transient} {
		sort.Slice(b.folds, func(i, j int) bool { return b.folds[i].seq < b.folds[j].seq })
		*b.total = len(b.folds)
		if len(b.folds) > limit {
			b.folds = b.folds[:limit]
			out.Truncated = true
		}
	}
	for _, f := range created.folds {
		out.EntitiesCreated = append(out.EntitiesCreated, entityOut(f.ent, false))
	}
	for _, f := range deleted.folds {
		out.EntitiesDeleted = append(out.EntitiesDeleted, entityOut(f.ent, true))
	}
	for _, f := range transient.folds {
		out.EntitiesTransient = append(out.EntitiesTransient, entityOut(f.ent, true))
	}
	for _, f := range changed.folds {
		keys := make([]string, 0, len(f.changedKeys))
		for k := range f.changedKeys {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out.EntitiesChanged = append(out.EntitiesChanged, EntityDiff{
			Entity: entityOut(f.ent, false), ChangedKeys: keys, StateChanged: f.stateChanged,
		})
	}

	type relationBucket struct {
		folds []*relationFold
		total *int
		dst   *[]Relation
	}
	relBuckets := []relationBucket{
		{total: &out.Totals.RelationsAdded, dst: &out.RelationsAdded},
		{total: &out.Totals.RelationsRemoved, dst: &out.RelationsRemoved},
		{total: &out.Totals.RelationsChanged, dst: &out.RelationsChanged},
		{total: &out.Totals.RelationsTransient, dst: &out.RelationsTransient},
	}
	for _, f := range relations {
		presentAtEnd := f.lastLifecycle != model.RelationRemoved
		switch {
		case f.addedFirst && presentAtEnd:
			relBuckets[0].folds = append(relBuckets[0].folds, f)
		case f.addedFirst && !presentAtEnd:
			relBuckets[3].folds = append(relBuckets[3].folds, f)
		case !presentAtEnd:
			relBuckets[1].folds = append(relBuckets[1].folds, f)
		case f.attrChanged:
			relBuckets[2].folds = append(relBuckets[2].folds, f)
		}
	}
	for i := range relBuckets {
		b := &relBuckets[i]
		sort.Slice(b.folds, func(x, y int) bool { return b.folds[x].seq < b.folds[y].seq })
		*b.total = len(b.folds)
		if len(b.folds) > limit {
			b.folds = b.folds[:limit]
			out.Truncated = true
		}
		for _, f := range b.folds {
			*b.dst = append(*b.dst, relationOut(f.rel))
		}
	}

	out.Summary = diffSummary(out)
	return nil, out, nil
}

// diffBounds resolves the window/from/to inputs into the two instants.
func (s *Server) diffBounds(in GraphDiffInput) (from, to time.Time, err error) {
	switch {
	case in.Window != "" && in.From != "":
		return time.Time{}, time.Time{}, fmt.Errorf("give either window or from, not both")
	case in.Window != "":
		d, derr := time.ParseDuration(in.Window)
		if derr != nil || d <= 0 {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid window %q: use a positive Go duration like 1h or 24h", in.Window)
		}
		now := s.now()
		return now.Add(-d), now, nil
	case in.From != "":
		from, err = parseOptTime(in.From, "from")
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		to = s.now()
		if in.To != "" {
			to, err = parseOptTime(in.To, "to")
			if err != nil {
				return time.Time{}, time.Time{}, err
			}
		}
		if !to.After(from) {
			return time.Time{}, time.Time{}, fmt.Errorf("to (%s) must be after from (%s)", formatTime(to), formatTime(from))
		}
		return from, to, nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("give a window (e.g. 24h) or a from instant")
	}
}

func diffSummary(o GraphDiffOutput) string {
	t := o.Totals
	if t == (DiffTotals{}) {
		return "No net change between the two instants."
	}
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	add(t.EntitiesCreated, "entities created")
	add(t.EntitiesDeleted, "entities deleted")
	add(t.EntitiesChanged, "entities changed")
	add(t.EntitiesTransient, "entities transient (appeared and disappeared)")
	add(t.RelationsAdded, "relations added")
	add(t.RelationsRemoved, "relations removed")
	add(t.RelationsChanged, "relations changed")
	add(t.RelationsTransient, "relations transient")
	return "Net difference: " + strings.Join(parts, ", ") + "."
}
