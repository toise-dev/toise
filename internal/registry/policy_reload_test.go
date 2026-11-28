package registry

import (
	"errors"
	"sync"
	"testing"

	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/tenant"
)

func openPolicyReg(t *testing.T, limits Limits) *Registry {
	t.Helper()
	reg, err := OpenWithLimits(t.TempDir(), store.DefaultConfig(), 64, limits, discard())
	if err != nil {
		t.Fatalf("OpenWithLimits: %v", err)
	}
	return reg
}

func TestSetTenantPolicyAdmitsNewTenant(t *testing.T) {
	reg := openPolicyReg(t, Limits{
		AutoCreate: true,
		Allowlist:  []string{"acme", "default"},
	})
	defer reg.Close()

	if _, err := reg.ForWrite("beta"); !errors.Is(err, tenant.ErrNotAllowed) {
		t.Fatalf("ForWrite(beta) before policy change: got %v, want tenant.ErrNotAllowed", err)
	}
	reg.SetTenantPolicy([]string{"acme", "beta", "default"}, 0)
	if _, err := reg.ForWrite("beta"); err != nil {
		t.Fatalf("ForWrite(beta) after policy change: %v", err)
	}
}

func TestSetTenantPolicyRemovalKeepsOpenTenant(t *testing.T) {
	reg := openPolicyReg(t, Limits{
		AutoCreate: true,
		Allowlist:  []string{"acme", "default"},
	})
	defer reg.Close()

	if _, err := reg.ForWrite("acme"); err != nil {
		t.Fatalf("ForWrite(acme): %v", err)
	}
	reg.SetTenantPolicy([]string{"default"}, 0)

	if _, ok := reg.Peek("acme"); !ok {
		t.Fatalf("Peek(acme) after removal: not open")
	}
	if _, err := reg.For("acme"); err != nil {
		t.Fatalf("For(acme) after removal: %v", err)
	}
	if _, err := reg.For("gamma"); !errors.Is(err, tenant.ErrNotAllowed) {
		t.Fatalf("For(gamma) after removal: got %v, want tenant.ErrNotAllowed", err)
	}
}

func TestSetTenantPolicyLoweredCapOnlyBlocksNew(t *testing.T) {
	reg := openPolicyReg(t, Limits{
		AutoCreate: true,
		MaxTenants: 0,
	})
	defer reg.Close()

	for _, id := range []string{"t1", "t2", "t3"} {
		if _, err := reg.ForWrite(id); err != nil {
			t.Fatalf("ForWrite(%s): %v", id, err)
		}
	}
	reg.SetTenantPolicy(nil, 1)

	if got := reg.TenantCount(); got != 4 {
		t.Fatalf("TenantCount after cap change: got %d, want 4 (default plus three)", got)
	}
	for _, id := range []string{"t1", "t2", "t3"} {
		if _, err := reg.For(id); err != nil {
			t.Fatalf("For(%s) after cap change: %v", id, err)
		}
	}
	if _, err := reg.ForWrite("t4"); !errors.Is(err, tenant.ErrNotAllowed) {
		t.Fatalf("ForWrite(t4) after cap change: got %v, want tenant.ErrNotAllowed", err)
	}
}

func TestSetTenantPolicyKeepsAutoCreateOff(t *testing.T) {
	reg := openPolicyReg(t, Limits{
		AutoCreate: false,
	})
	defer reg.Close()

	reg.SetTenantPolicy([]string{"x"}, 5)
	if _, err := reg.ForWrite("x"); !errors.Is(err, tenant.ErrNotAllowed) {
		t.Fatalf("ForWrite(x): got %v, want tenant.ErrNotAllowed", err)
	}
}

func TestSetTenantPolicyCopiesAllowlist(t *testing.T) {
	reg := openPolicyReg(t, Limits{
		AutoCreate: true,
	})
	defer reg.Close()

	allow := []string{"orig"}
	reg.SetTenantPolicy(allow, 0)
	allow[0] = "mutated"

	if _, err := reg.ForWrite("orig"); err != nil {
		t.Fatalf("ForWrite(orig) after caller mutated slice: %v", err)
	}
	if _, err := reg.ForWrite("mutated"); !errors.Is(err, tenant.ErrNotAllowed) {
		t.Fatalf("ForWrite(mutated): got %v, want tenant.ErrNotAllowed", err)
	}
}

func TestSetTenantPolicyConcurrentWithRequests(t *testing.T) {
	reg := openPolicyReg(t, Limits{
		AutoCreate: true,
		Allowlist:  []string{"acme", "default"},
	})
	defer reg.Close()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := reg.ForWrite("acme"); err != nil {
					t.Errorf("worker %d: ForWrite(acme): %v", n, err)
					return
				}
				if _, err := reg.ForWrite("off"); !errors.Is(err, tenant.ErrNotAllowed) {
					t.Errorf("worker %d: ForWrite(off): got %v, want tenant.ErrNotAllowed", n, err)
					return
				}
			}
		}(i)
	}

	for i := 0; i < 2000; i++ {
		if i%2 == 0 {
			reg.SetTenantPolicy([]string{"acme", "default"}, 0)
		} else {
			reg.SetTenantPolicy([]string{"acme", "default", "other"}, 10)
		}
	}
	close(stop)
	wg.Wait()
}
