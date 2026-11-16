package registry

import (
	"testing"

	"github.com/toise-dev/toise/internal/store"
)

func TestTombstoneCapReachesBootAndMintedStacks(t *testing.T) {
	reg, err := OpenWithLimits(t.TempDir(), store.DefaultConfig(), 0, Limits{
		AutoCreate:   true,
		TombstoneCap: 5000,
	}, discard())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	if got := reg.Stacks()[0].Graph.TombstoneCap(); got != 5000 {
		t.Errorf("boot-opened stack tombstone cap = %d, want 5000", got)
	}

	st, err := reg.For("acme")
	if err != nil {
		t.Fatalf("minting tenant: %v", err)
	}
	if got := st.Graph.TombstoneCap(); got != 5000 {
		t.Errorf("later-minted stack tombstone cap = %d, want 5000", got)
	}
}

func TestTombstoneCapZeroKeepsTheDefault(t *testing.T) {
	reg, err := OpenWithLimits(t.TempDir(), store.DefaultConfig(), 0, Limits{AutoCreate: true}, discard())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	if got := reg.Stacks()[0].Graph.TombstoneCap(); got != 1024 {
		t.Errorf("default tombstone cap = %d, want 1024", got)
	}

	st, err := reg.For("acme")
	if err != nil {
		t.Fatalf("minting tenant: %v", err)
	}
	if got := st.Graph.TombstoneCap(); got != 1024 {
		t.Errorf("later-minted stack default tombstone cap = %d, want 1024", got)
	}
}
