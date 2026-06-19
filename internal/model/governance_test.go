package model

import "testing"

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

	// The two reused semconv keys must be flagged as such; the entity.* keys must not.
	for k, wantSemconv := range map[string]bool{
		"service.namespace":       true,
		"service.criticality":     true,
		"entity.owner.team":       false,
		"entity.location.site":    false,
		"entity.lifecycle.status": false,
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
