package model

import (
	"sort"
	"testing"

	"github.com/toise-dev/toise/pkg/emit/wire"
)

// TestWireVocabularyMirror pins the vocabulary the emit SDK spells independently
// against the engine registry. The SDK's wire package is stdlib-only and cannot
// import this package, so every entity-type and relation-type literal is
// duplicated there.
//
// The check is deliberately exhaustive in both directions rather than a list of
// pairs: a list only catches drift in constants somebody remembered to add to
// it, which is how the two spellings grew apart one literal at a time in the
// first place. A type registered here but absent from wire leaves producers
// unable to name it; a type in wire but not registered here would be refused at
// the boundary after a producer trusted it.
func TestWireVocabularyMirror(t *testing.T) {
	t.Run("entity types", func(t *testing.T) {
		assertSameSet(t, "entity type", EntityTypes(), wire.EntityTypes())
	})

	t.Run("relation types", func(t *testing.T) {
		registered := make([]string, 0, len(relationTypes))
		for _, def := range RelationTypes() {
			registered = append(registered, def.Type)
		}
		assertSameSet(t, "relation type", registered, wire.RelationTypes())
	})
}

func assertSameSet(t *testing.T, what string, model, wireSide []string) {
	t.Helper()

	inWire := make(map[string]bool, len(wireSide))
	for _, v := range wireSide {
		if inWire[v] {
			t.Errorf("%s %q is listed twice in wire", what, v)
		}
		inWire[v] = true
	}
	inModel := make(map[string]bool, len(model))
	for _, v := range model {
		inModel[v] = true
	}

	var missing, extra []string
	for _, v := range model {
		if !inWire[v] {
			missing = append(missing, v)
		}
	}
	for _, v := range wireSide {
		if !inModel[v] {
			extra = append(extra, v)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 {
		t.Errorf("%ss registered here but absent from pkg/emit/wire: %v — a producer cannot name them", what, missing)
	}
	if len(extra) > 0 {
		t.Errorf("%ss in pkg/emit/wire but not registered here: %v — the boundary would refuse them", what, extra)
	}
}
