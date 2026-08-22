package registry

import (
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/store"
)

// TestSetResurrectionGraceCoversEveryStack pins the wiring #344's decision
// depends on: the configured grace must reach the stacks boot opened eagerly
// AND every stack minted afterwards. Missing either half would give two
// tenants different resurrection behavior from one config file — the exact
// kind of silent divergence the knob exists to prevent.
func TestSetResurrectionGraceCoversEveryStack(t *testing.T) {
	reg, err := Open(t.TempDir(), store.DefaultConfig(), 0, discard())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	before := reg.Stacks()[0].Graph.TombstoneTTL()
	reg.SetResurrectionGrace(42 * time.Minute)

	if got := reg.Stacks()[0].Graph.TombstoneTTL(); got != 42*time.Minute {
		t.Errorf("boot-opened stack grace = %v, want 42m (was %v)", got, before)
	}

	st, err := reg.For("acme")
	if err != nil {
		t.Fatalf("minting tenant: %v", err)
	}
	if got := st.Graph.TombstoneTTL(); got != 42*time.Minute {
		t.Errorf("later-minted stack grace = %v, want 42m", got)
	}
}

// TestSetResurrectionGraceZeroKeepsTheDefault pins the config contract: zero
// means "not set", so an absent key changes nothing — the zero-config posture
// stays byte-identical.
func TestSetResurrectionGraceZeroKeepsTheDefault(t *testing.T) {
	reg, err := Open(t.TempDir(), store.DefaultConfig(), 0, discard())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	def := reg.Stacks()[0].Graph.TombstoneTTL()
	if def <= 0 {
		t.Fatalf("default grace = %v; expected the built-in positive default", def)
	}
	reg.SetResurrectionGrace(0)
	if got := reg.Stacks()[0].Graph.TombstoneTTL(); got != def {
		t.Errorf("zero changed the grace to %v; zero must mean untouched", got)
	}
}

// TestSetResurrectionGraceNegativeDisables pins the documented meaning of a
// negative value: the time bound is off and only the cap evicts tombstones.
func TestSetResurrectionGraceNegativeDisables(t *testing.T) {
	reg, err := Open(t.TempDir(), store.DefaultConfig(), 0, discard())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	reg.SetResurrectionGrace(-1)
	if got := reg.Stacks()[0].Graph.TombstoneTTL(); got >= 0 {
		t.Errorf("negative grace stored as %v; the disable semantics rely on it staying negative", got)
	}
}
