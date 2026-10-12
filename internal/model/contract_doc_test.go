package model

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// identityUnspecified names registered entity types whose identity no document
// states, and says why. Adding a line here is a decision, not a formality: a
// type a producer can emit with no documented identity is a type nobody can
// emit correctly, and two producers will key it two ways.
//
//   - pod: blocked on a naming decision. The OpenTelemetry entity is k8s.pod,
//     identified by k8s.pod.uid, while this registry calls the type pod. Writing
//     an identity down before that is settled would commit us to the divergence
//     (semconv model/k8s/entities.yaml).
var identityUnspecified = map[string]string{
	"pod": "blocked on the pod vs k8s.pod naming decision",
}

// TestEveryRegisteredTypeHasADocumentedIdentity reads the data-model documents
// and fails on a registered entity type none of them gives an identity row.
//
// It exists because the contract and the code have disagreed three times in one
// week, each time costing a wrong answer to a producer: a relation type the
// contract called unregistered had been registered for four versions; the
// registry declares one endpoint pair for runs_on where the contract declares
// three; and a type was registered with nothing documenting it. Prose cannot be
// checked, but a vocabulary can, and the vocabulary is where the costly
// disagreements were.
func TestEveryRegisteredTypeHasADocumentedIdentity(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "data-model")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading %s: %v", root, err)
	}

	row := regexp.MustCompile("(?m)^\\| ?`([a-z][a-z0-9._]*)`")
	documented := map[string]bool{}
	files := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(root, e.Name()))
		if rerr != nil {
			t.Fatalf("reading %s: %v", e.Name(), rerr)
		}
		files++
		for _, m := range row.FindAllStringSubmatch(string(b), -1) {
			documented[m[1]] = true
		}
	}
	if files == 0 {
		t.Fatal("no data-model documents found; this test would pass by reading nothing")
	}
	// A positive control: the type every producer starts from must be found, or
	// the extraction is broken and every other result is meaningless.
	if !documented[TypeHost] {
		t.Fatalf("the extraction found no identity row for %q across %d documents; it is broken, not the docs", TypeHost, files)
	}

	for typ := range entityTypes {
		if documented[typ] {
			continue
		}
		if why, declared := identityUnspecified[typ]; declared {
			t.Logf("%s: identity deliberately unspecified — %s", typ, why)
			continue
		}
		t.Errorf("entity type %q is registered and no data-model document gives its identity; "+
			"a producer can emit it and has nothing telling it which attributes identify it", typ)
	}

	// The reverse: a declared exception that has since been documented must be
	// removed, or the list becomes a place where decisions go to be forgotten.
	for typ := range identityUnspecified {
		if documented[typ] {
			t.Errorf("entity type %q is documented now; remove it from identityUnspecified", typ)
		}
	}
}
