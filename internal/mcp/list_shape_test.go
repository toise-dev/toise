package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/toise-dev/toise/internal/model"
)

// TestListFieldsSayWhichKindOfEmpty pins a rule the tool-contract golden cannot
// see, because the golden records field names and types and not whether a field
// is rendered at all.
//
// Measured on production by the operating session: `attributes` was ABSENT on
// 181 of 293 entities in one tenant and 211 of 241 in another — 62 % and 88 %.
// A consumer indexing the field crashed on the majority of entities, and one
// defaulting it could not tell "this entity has no descriptive attributes" from
// "this field was not rendered at this verbosity". Those are different answers,
// and an absent field gave them the same shape.
//
// So: an empty list means asked and none, null means not rendered here, and the
// field is never absent. An endpoint is the case that makes the first one real —
// the contract gives it no descriptive attributes by design.
func TestListFieldsSayWhichKindOfEmpty(t *testing.T) {
	endpoint := model.Entity{ID: "01E", Type: model.TypeNetworkEndpoint, Identity: []model.KeyValue{
		{Key: "server.address", Value: model.StringValue("10.0.0.1")},
	}}

	full, err := json.Marshal(entityOutV(endpoint, false, false))
	if err != nil {
		t.Fatal(err)
	}
	compact, err := json.Marshal(entityOutV(endpoint, false, true))
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		what string
		in   []byte
		want string
	}{
		// Full verbosity: the endpoint has no descriptive attributes, and says so.
		{"full renders an empty list for none", full, `"attributes":[]`},
		{"full renders the identity it has", full, `"identity":[{"key":"server.address"`},
		// Compact: not rendered, and distinguishable from none.
		{"compact renders null, not an absent field", compact, `"attributes":null`},
		{"compact renders a null identity too", compact, `"identity":null`},
	} {
		if !strings.Contains(string(c.in), c.want) {
			t.Errorf("%s: %s not found in %s", c.what, c.want, c.in)
		}
	}

	// And the field is never missing, in either verbosity: that is the whole point.
	for _, in := range [][]byte{full, compact} {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(in, &m); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"identity", "attributes", "asserted_by", "collected_by"} {
			if _, ok := m[k]; !ok {
				t.Errorf("%s is absent from %s; absence is the one answer these fields must never give", k, in)
			}
		}
	}
}
