package wire

import "testing"

// TestVocabularyLists guards the two accessors against the mistake they exist to
// prevent: a constant added to the package but forgotten in the list it belongs
// to, which would leave a producer unable to discover a type that Toise accepts.
// The engine-side cross-check proves the sets match the registry; this one keeps
// the lists internally honest, inside the module a producer actually imports.
func TestVocabularyLists(t *testing.T) {
	for _, tc := range []struct {
		what string
		list []string
		want []string
	}{
		{
			what: "entity type",
			list: EntityTypes(),
			want: []string{TypeHost, TypeProcess, TypeNetworkInterface, TypeNetworkAddress,
				TypeNetworkRoute, TypeServiceListener, TypeServiceInstance, TypeDatabase,
				TypeNetworkDevice, TypeNetworkEndpoint, TypeComputeVM, TypeContainer,
				TypePod},
		},
		{
			what: "relation type",
			list: RelationTypes(),
			want: []string{RelTypeRunsOn, RelTypeHasInterface, RelTypeBoundTo, RelTypeNextHopVia,
				RelTypeListensOn, RelTypeMonitors, RelTypeHasRoute, RelTypeConnectedTo,
				RelTypeDependsOn, RelTypeSameAs, RelTypeRoutesVia, RelTypeForwardsTo, RelTypeAdjacentTo},
		},
	} {
		seen := map[string]bool{}
		for _, v := range tc.list {
			if v == "" {
				t.Errorf("%s list carries an empty value", tc.what)
			}
			if seen[v] {
				t.Errorf("%s %q is listed twice", tc.what, v)
			}
			seen[v] = true
		}
		for _, v := range tc.want {
			if !seen[v] {
				t.Errorf("%s %q is declared but missing from the list", tc.what, v)
			}
		}
		if len(tc.list) != len(tc.want) {
			t.Errorf("%s list has %d entries, want %d", tc.what, len(tc.list), len(tc.want))
		}
	}
}

// TestRelationshipDescriptorKeysAreEntityKeys pins the deliberate aliasing: a
// relationship descriptor is a miniature entity reference, so its target keys
// are spelled exactly like the record-level ones.
func TestRelationshipDescriptorKeysAreEntityKeys(t *testing.T) {
	if RelTargetType != AttrEntityType || RelTargetID != AttrEntityID {
		t.Errorf("descriptor keys drifted from the record-level keys: %q/%q vs %q/%q",
			RelTargetType, RelTargetID, AttrEntityType, AttrEntityID)
	}
}
