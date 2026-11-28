package config

import (
	"reflect"
	"testing"
)

func TestIgnoredChangesNilWhenIdentical(t *testing.T) {
	t.Parallel()
	c := Default()
	if got := c.IgnoredChanges(c); got != nil {
		t.Fatalf("IgnoredChanges(identical) = %v, want nil", got)
	}
}

func TestIgnoredChangesSkipsReloadableKeys(t *testing.T) {
	t.Parallel()
	c := Default()
	next := Default()
	next.TenantAllowlist = []string{"fake-tenant-1:fake-pattern-1"}
	next.MaxTenants = 12345
	next.TenantNames = []string{"fake-tenant-2:fake-name-2"}
	next.TenantRetentionMaxAge = []string{"fake-tenant-3:fake-age-3"}
	next.TenantTokens = []string{"fake-tenant-4:fake-token-4"}
	next.TenantReadTokens = []string{"fake-tenant-5:fake-read-token-5"}
	next.TenantIngestTokens = []string{"fake-tenant-6:fake-ingest-token-6"}
	if got := c.IgnoredChanges(next); got != nil {
		t.Fatalf("IgnoredChanges(reloadable-only) = %v, want nil", got)
	}
}

func TestIgnoredChangesNamesRestartKeys(t *testing.T) {
	t.Parallel()
	c := Default()
	next := Default()
	next.Listen = "fake-listen-addr-1"
	next.LogLevel = "fake-log-level-1"
	next.MaxTenants = 54321
	got := c.IgnoredChanges(next)
	want := []string{"listen", "log_level"}
	if len(got) != len(want) {
		t.Fatalf("IgnoredChanges = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("IgnoredChanges = %v, want %v", got, want)
		}
	}
	for _, key := range got {
		if key == next.Listen || key == next.LogLevel {
			t.Fatalf("IgnoredChanges leaked value %q", key)
		}
	}
}

func TestIgnoredChangesSkipsUntaggedFields(t *testing.T) {
	t.Parallel()
	c := Default()
	next := Default()
	next.ProductionOverrides = []string{"fake-override-1"}
	if got := c.IgnoredChanges(next); got != nil {
		t.Fatalf("IgnoredChanges(untagged-only) = %v, want nil", got)
	}
}

func TestReloadableKeysAreRealYAMLKeys(t *testing.T) {
	t.Parallel()
	v := reflect.TypeOf(Config{})
	tags := make(map[string]bool)
	for i := 0; i < v.NumField(); i++ {
		tag := v.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name := tag
		for j := 0; j < len(tag); j++ {
			if tag[j] == ',' {
				name = tag[:j]
				break
			}
		}
		tags[name] = true
	}
	for _, key := range ReloadableKeys {
		if !tags[key] {
			t.Errorf("ReloadableKeys entry %q does not match any yaml tag of Config", key)
		}
	}
}
