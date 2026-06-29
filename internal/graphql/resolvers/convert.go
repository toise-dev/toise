package resolvers

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	"github.com/toise-dev/toise/internal/graphql/generated"
	"github.com/toise-dev/toise/internal/model"
)

var changeTypeGQL = map[model.ChangeType]generated.ChangeType{
	model.EntityCreated:            generated.ChangeTypeEntityCreated,
	model.EntityDeleted:            generated.ChangeTypeEntityDeleted,
	model.EntityIdentityChanged:    generated.ChangeTypeEntityIdentityChanged,
	model.EntityAttributeUpdated:   generated.ChangeTypeEntityAttributeUpdated,
	model.EntityStateChanged:       generated.ChangeTypeEntityStateChanged,
	model.EntityUnchanged:          generated.ChangeTypeEntityUnchanged,
	model.RelationAdded:            generated.ChangeTypeRelationAdded,
	model.RelationRemoved:          generated.ChangeTypeRelationRemoved,
	model.RelationAttributeChanged: generated.ChangeTypeRelationAttributeChanged,
}

func valueToGQL(v model.Value) (string, generated.ValueType) {
	switch v.Kind() {
	case model.KindInt:
		return strconv.FormatInt(v.Int(), 10), generated.ValueTypeInt
	case model.KindDouble:
		return strconv.FormatFloat(v.Double(), 'g', -1, 64), generated.ValueTypeDouble
	case model.KindBool:
		return strconv.FormatBool(v.Bool()), generated.ValueTypeBool
	default:
		return v.Str(), generated.ValueTypeString
	}
}

func attrsToGQL(kvs []model.KeyValue) []generated.Attribute {
	out := make([]generated.Attribute, len(kvs))
	for i, kv := range kvs {
		val, vt := valueToGQL(kv.Value)
		out[i] = generated.Attribute{Key: kv.Key, Value: val, Type: vt}
	}
	return out
}

func entityToGQL(e model.Entity, deleted bool) *generated.Entity {
	return &generated.Entity{
		ID:         string(e.ID),
		Type:       e.Type,
		Identity:   attrsToGQL(e.Identity),
		Attributes: attrsToGQL(e.Attributes),
		SchemaURL:  e.SchemaURL,
		Deleted:    deleted,
	}
}

func relationToGQL(r model.Relation) *generated.Relation {
	return &generated.Relation{
		ID:         string(r.ID),
		Type:       r.Type,
		FromID:     string(r.From),
		ToID:       string(r.To),
		Attributes: attrsToGQL(r.Attributes),
		Structural: r.Structural,
	}
}

func eventToChangeGQL(ev model.Event) *generated.ChangeEvent {
	ce := &generated.ChangeEvent{ChangedKeys: []string{}}
	switch {
	case ev.Entity != nil:
		ee := ev.Entity
		ce.ID = ee.EventID
		ce.ChangeType = changeTypeGQL[ee.ChangeType]
		ce.EventTime = ee.EventTime.UTC().Format(time.RFC3339Nano)
		ce.RecordedAt = ee.RecordedAt.UTC().Format(time.RFC3339Nano)
		ce.SchemaVersion = ee.SchemaVersion
		if ee.ChangedKeys != nil {
			ce.ChangedKeys = ee.ChangedKeys
		}
		if ee.DeleteReason != "" {
			r := ee.DeleteReason
			ce.DeleteReason = &r
		}
		ce.Entity = entityToGQL(ee.Entity, ee.ChangeType == model.EntityDeleted)
	case ev.Relation != nil:
		re := ev.Relation
		ce.ID = re.EventID
		ce.ChangeType = changeTypeGQL[re.ChangeType]
		ce.EventTime = re.EventTime.UTC().Format(time.RFC3339Nano)
		ce.RecordedAt = re.RecordedAt.UTC().Format(time.RFC3339Nano)
		ce.SchemaVersion = re.SchemaVersion
		if re.ChangedKeys != nil {
			ce.ChangedKeys = re.ChangedKeys
		}
		ce.Relation = relationToGQL(re.Relation)
	}
	return ce
}

func eventID(ev model.Event) string {
	switch {
	case ev.Entity != nil:
		return ev.Entity.EventID
	case ev.Relation != nil:
		return ev.Relation.EventID
	default:
		return ""
	}
}

// --- Relay cursor pagination ---

func encodeCursor(id string) string {
	return base64.StdEncoding.EncodeToString([]byte(id))
}

func decodeCursor(c string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(c)
	if err != nil {
		return "", fmt.Errorf("invalid cursor %q: pass an `endCursor` value returned by a previous page", c)
	}
	return string(b), nil
}

// paginate returns a slice of items after the given cursor, capped at first
// (default 50), along with the end cursor and whether more pages remain.
func paginate[T any](items []T, idOf func(T) string, first *int, after *string) (page []T, endCursor *string, hasNext bool, err error) {
	start := 0
	if after != nil && *after != "" {
		cur, derr := decodeCursor(*after)
		if derr != nil {
			return nil, nil, false, derr
		}
		start = len(items)
		for i, it := range items {
			if idOf(it) == cur {
				start = i + 1
				break
			}
		}
	}
	const maxFirst = 200 // same page bound as the MCP tools (#144)
	n := 50
	if first != nil {
		n = *first
	}
	switch {
	case n < 0:
		n = 0
	case n > maxFirst:
		n = maxFirst
	}
	if start > len(items) {
		start = len(items)
	}
	end := start + n
	if end > len(items) {
		end = len(items)
	}
	page = items[start:end]
	hasNext = end < len(items)
	if len(page) > 0 {
		c := encodeCursor(idOf(page[len(page)-1]))
		endCursor = &c
	}
	return page, endCursor, hasNext, nil
}
