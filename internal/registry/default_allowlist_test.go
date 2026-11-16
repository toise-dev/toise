package registry

import (
	"errors"
	"testing"

	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/tenant"
)

func TestDefaultAllowlistWritePolicy(t *testing.T) {
	t.Run("no allowlist", func(t *testing.T) {
		reg, err := OpenWithLimits(t.TempDir(), store.DefaultConfig(), 0, Limits{AutoCreate: true}, discard())
		if err != nil {
			t.Fatal(err)
		}
		if !reg.DefaultWritable() {
			t.Fatal("DefaultWritable() = false, want true")
		}
		for _, id := range []string{"", tenant.Default} {
			if _, err := reg.ForWrite(id); err != nil {
				t.Fatalf("ForWrite(%q) unexpected error: %v", id, err)
			}
		}
	})

	t.Run("allowlist without default", func(t *testing.T) {
		reg, err := OpenWithLimits(t.TempDir(), store.DefaultConfig(), 0, Limits{
			AutoCreate: true,
			Allowlist:  []string{"1", "3"},
		}, discard())
		if err != nil {
			t.Fatal(err)
		}
		if reg.DefaultWritable() {
			t.Fatal("DefaultWritable() = true, want false")
		}
		_, err = reg.ForWrite(tenant.Default)
		if !errors.Is(err, tenant.ErrNotAllowed) {
			t.Fatalf("ForWrite(%q) error = %v, want ErrNotAllowed", tenant.Default, err)
		}
		if _, err := reg.ForWrite("1"); err != nil {
			t.Fatalf("ForWrite(%q) unexpected error: %v", "1", err)
		}
		if _, err := reg.For(tenant.Default); err != nil {
			t.Fatalf("For(%q) unexpected error: %v", tenant.Default, err)
		}
	})

	t.Run("allowlist includes default", func(t *testing.T) {
		reg, err := OpenWithLimits(t.TempDir(), store.DefaultConfig(), 0, Limits{
			AutoCreate: true,
			Allowlist:  []string{"1", tenant.Default},
		}, discard())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reg.ForWrite(tenant.Default); err != nil {
			t.Fatalf("ForWrite(%q) unexpected error: %v", tenant.Default, err)
		}
	})
}
