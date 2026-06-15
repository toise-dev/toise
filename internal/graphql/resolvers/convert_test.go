package resolvers

import (
	"testing"

	"github.com/toise-dev/toise/internal/model"
)

// TestChangeTypeGQLExhaustive pins #166: every real model.ChangeType maps to a
// GraphQL ChangeType. A new change type added to the model (const block +
// changeTypeNames) but not to changeTypeGQL would otherwise surface as an empty
// ChangeType on the wire. The loop walks the contiguous iota values while they
// name a real type (String() != "unspecified").
func TestChangeTypeGQLExhaustive(t *testing.T) {
	n := 0
	for ct := model.EntityCreated; ct.String() != "unspecified"; ct++ {
		if changeTypeGQL[ct] == "" {
			t.Errorf("changeTypeGQL has no mapping for %s (%d)", ct, int(ct))
		}
		n++
	}
	if n != len(changeTypeGQL) {
		t.Errorf("walked %d real change types but changeTypeGQL has %d entries — a stale or extra mapping", n, len(changeTypeGQL))
	}
}
