package config

import (
	"reflect"
	"sort"
	"strings"
)

// ReloadableKeys lists, in this order, the configuration keys a running server re-applies on reload.
var ReloadableKeys = []string{"tenant_allowlist", "max_tenants", "tenant_names", "tenant_retention_max_age", "tenant_tokens", "tenant_read_tokens", "tenant_ingest_tokens"}

// IgnoredChanges returns the sorted yaml keys of fields that differ between c and next but are not re-applied on reload, or nil if none.
func (c Config) IgnoredChanges(next Config) []string {
	reloadable := make(map[string]bool, len(ReloadableKeys))
	for _, k := range ReloadableKeys {
		reloadable[k] = true
	}

	var ignored []string
	v := reflect.ValueOf(c)
	n := reflect.ValueOf(next)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		key, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if key == "" || key == "-" || reloadable[key] {
			continue
		}
		if !reflect.DeepEqual(v.Field(i).Interface(), n.Field(i).Interface()) {
			ignored = append(ignored, key)
		}
	}
	sort.Strings(ignored)
	return ignored
}
