package model

import (
	"testing"

	"github.com/toise-dev/toise/pkg/emit/wire"
)

// TestWireVocabularyMirror pins the value-level vocabulary the emit SDK spells
// independently against the engine registry. The SDK's wire package is stdlib-only
// and cannot import this package, so each relationship-type / entity-type literal is
// duplicated there; this test is the tripwire that keeps the two spellings from
// drifting apart one constant at a time.
func TestWireVocabularyMirror(t *testing.T) {
	cases := []struct {
		name        string
		wire, model string
	}{
		{"same_as", wire.RelTypeSameAs, RelSameAs},
		{"depends_on", wire.RelTypeDependsOn, RelDependsOn},
		{"network.endpoint", wire.TypeNetworkEndpoint, TypeNetworkEndpoint},
	}
	for _, c := range cases {
		if c.wire != c.model {
			t.Errorf("%s: wire %q != model %q — the SDK and engine vocabularies drifted", c.name, c.wire, c.model)
		}
	}
}
