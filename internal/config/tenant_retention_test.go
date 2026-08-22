package config

import (
	"testing"
	"time"
)

// TestTenantRetentionMap pins the parse contract for per-tenant retention
// (#350): pairs follow the tenant-token precedent, and every malformed shape is
// a hard error — a typo here would prune a paying tenant's history to the wrong
// bound, silently, which is the same accident class as a token pair widening
// access.
func TestTenantRetentionMap(t *testing.T) {
	c := Config{TenantRetentionMaxAge: []string{"imagroupe:2160h", "acme:168h"}}
	m, err := c.TenantRetentionMap()
	if err != nil {
		t.Fatalf("valid pairs rejected: %v", err)
	}
	if m["imagroupe"] != 2160*time.Hour || m["acme"] != 168*time.Hour {
		t.Errorf("parsed map = %v", m)
	}

	if m, err := (Config{}).TenantRetentionMap(); err != nil || m != nil {
		t.Errorf("absent setting must be a nil map with no error, got %v / %v", m, err)
	}

	for _, bad := range []string{
		"imagroupe",           // no duration
		"imagroupe:",          // empty duration
		"imagroupe:soon",      // not a duration
		"imagroupe:-24h",      // negative: use the global bound, not a pair
		"imagroupe:0s",        // zero would silently mean "unlimited" — refuse
		":720h",               // no tenant
		"bad tenant:720h",     // not a canonical tenant id
		"IMAGROUPE\ttab:720h", // sanitization must not quietly rewrite the id
	} {
		if _, err := (Config{TenantRetentionMaxAge: []string{bad}}).TenantRetentionMap(); err == nil {
			t.Errorf("pair %q accepted; every malformed pair must refuse to boot", bad)
		}
	}

	dup := Config{TenantRetentionMaxAge: []string{"acme:24h", "acme:48h"}}
	if _, err := dup.TenantRetentionMap(); err == nil {
		t.Error("duplicate tenant accepted; which bound wins would be silent")
	}
}
