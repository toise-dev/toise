package model

import (
	"slices"
	"strings"
	"testing"
)

func TestGovernanceAttributes(t *testing.T) {
	got := GovernanceAttributes()
	if len(got) == 0 {
		t.Fatal("governance vocabulary must not be empty")
	}

	byKey := map[string]GovernanceAttribute{}
	for _, a := range got {
		if a.Key == "" || a.Summary == "" {
			t.Errorf("%+v: every governance attribute needs a key and a summary", a)
		}
		if _, dup := byKey[a.Key]; dup {
			t.Errorf("duplicate governance key %q", a.Key)
		}
		byKey[a.Key] = a
	}

	// The two reused semconv keys must be flagged as such; the keys we invented
	// must not, and they must live under senhub.* rather than in someone else's
	// namespace.
	for k, wantSemconv := range map[string]bool{
		"service.namespace":       true,
		"service.criticality":     true,
		"senhub.owner.team":       false,
		"senhub.location.site":    false,
		"senhub.lifecycle.status": false,
	} {
		a, ok := byKey[k]
		if !ok {
			t.Errorf("governance vocabulary missing %q", k)
			continue
		}
		if a.Semconv != wantSemconv {
			t.Errorf("%q semconv = %v, want %v", k, a.Semconv, wantSemconv)
		}
	}

	// Enum keys carry well-known values.
	if vals := byKey["service.criticality"].Values; len(vals) == 0 {
		t.Error("service.criticality should advertise its well-known values")
	}

	// No key we invented may sit under a namespace semconv already owns: the
	// naming rule forbids it, and a later upstream definition of the same key
	// would make field data mean two things with nothing to separate them.
	for _, a := range got {
		if a.Semconv {
			continue
		}
		for _, ns := range []string{"entity.", "device.", "host.", "service.", "db.", "network.", "hw.", "process.", "k8s.", "cloud.", "os.", "telemetry."} {
			if strings.HasPrefix(a.Key, ns) {
				t.Errorf("%q is ours but sits under the semconv namespace %q", a.Key, ns)
			}
		}
	}

	// A renamed key keeps its old spelling reachable: the engine stored whatever
	// producers sent, so data emitted before the rename is still in the graph and
	// a consumer reading history needs the key to find as well as the key to use.
	for _, want := range []struct{ key, was string }{
		{"senhub.owner.team", "entity.owner.team"},
		{"senhub.owner.contact", "entity.owner.contact"},
		{"senhub.location.site", "entity.location.site"},
		{"senhub.lifecycle.status", "entity.lifecycle.status"},
	} {
		a, ok := byKey[want.key]
		if !ok {
			t.Errorf("missing %q", want.key)
			continue
		}
		if !slices.Contains(a.Was, want.was) {
			t.Errorf("%q does not list its previous spelling %q, got %v", want.key, want.was, a.Was)
		}
	}
}

// TestGovernanceAttributesIsCopy pins that callers cannot mutate the registry
// through the returned slice.
func TestGovernanceAttributesIsCopy(t *testing.T) {
	first := GovernanceAttributes()
	if len(first) == 0 {
		t.Fatal("empty vocabulary")
	}
	first[0].Key = "tampered"
	if GovernanceAttributes()[0].Key == "tampered" {
		t.Error("GovernanceAttributes returned a shared backing array; callers can corrupt the registry")
	}
}
