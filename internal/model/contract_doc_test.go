package model

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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

// identityForms is every identity key set the data-model documents are allowed
// to state, with what it identifies. A type may legitimately have more than one
// form — a route on a host and a route on a polled device are keyed
// differently — so the test cannot infer agreement; what it can do is make
// every form a declaration.
//
// What it still cannot catch, stated here rather than left to be rediscovered: a
// type with SEVERAL identity forms passes once ONE of them is documented. The
// host-side network.interface identity was absent from every document for
// months while the device-side form satisfied both this test and
// TestEveryRegisteredTypeHasADocumentedIdentity — a producer emitting a host's
// interfaces had nothing telling it which attributes identify them. Found by a
// reviewer asking why the form was missing from a list, not by a test. Adding a
// form here is the only thing that makes a document obliged to state it, so a
// new identity shape belongs in this map at the moment it is decided, not when
// someone notices.
//
// It exists because presence was not enough. The identity row for a route said
// one thing in the producer contract and another in the mapping document for
// four versions: the contract keys a route on its next hop, with the reason
// written out (RFC 2096 indexes on it, and without it a host with two uplinks
// collapses its two default routes into one entity whose gateway oscillates at
// poll cadence), while the mapping document left the next hop out of the
// identity and listed it as a descriptive attribute. Both were readable, one
// was wrong, and a producer following the wrong one builds a graph that reports
// a topology change every poll.
var identityForms = map[string]string{
	"host.id":                                         "host",
	"service.instance.id":                             "service.instance",
	"db.instance.id":                                  "db",
	"container.id":                                    "container",
	"k8s.node.uid":                                    "k8s.node — the cluster's view of a machine, joined to its host by a same_as belief",
	"host.id,host.service.name":                       "host.service, scoped to its machine — a service name is unique only within one host, and the name carries its full supervisor suffix",
	"network.device.id":                               "network.device",
	"network.segment.id":                              "network.segment",
	"host.id,mac":                                     "network.neighbor, scoped to the observing host",
	"mac,network.device.id":                           "network.neighbor, scoped to the observing device — a MAC learned on a port",
	"host.id,vmid":                                    "compute.vm, a VM seen from its hypervisor",
	"process.creation.time,process.pid":               "process",
	"host.id,network.interface.name":                  "network.interface, on a host",
	"network.device.id,network.interface.name":        "network.interface, a port on a polled device",
	"host.id,next_hop.ip,route.destination":           "network.route, on a host",
	"network.device.id,next_hop.ip,route.destination": "network.route, on a polled device",
	// Not an identity: the pair the retired adjacent_to carried as edge
	// attributes, quoted in the contract as the shape ADR 0022 forbids.
	"local_port,remote_port": "not an identity — the retired adjacent_to edge attributes, quoted to be refused",
}

// identityKeySet matches a brace-delimited set of dotted lowercase keys, which
// is how every data-model document writes an identity.
var identityKeySet = regexp.MustCompile(`\{([a-z][a-z0-9._]*(?:\s*,\s*[a-z][a-z0-9._]*)*)\}`)

// TestIdentityFormsAreDeclared fails on an identity key set no one declared,
// which is what a document disagreeing with another looks like.
func TestIdentityFormsAreDeclared(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "data-model")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading %s: %v", root, err)
	}
	seen := map[string][]string{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(root, e.Name()))
		if rerr != nil {
			t.Fatalf("reading %s: %v", e.Name(), rerr)
		}
		for _, m := range identityKeySet.FindAllStringSubmatch(string(b), -1) {
			keys := strings.Split(m[1], ",")
			for i := range keys {
				keys[i] = strings.TrimSpace(keys[i])
			}
			sort.Strings(keys)
			k := strings.Join(keys, ",")
			seen[k] = append(seen[k], e.Name())
		}
	}
	// A positive control: the form every other one is read against.
	if _, ok := seen["host.id"]; !ok {
		t.Fatal("the extraction found no {host.id} anywhere; it is broken, not the docs")
	}
	for form, files := range seen {
		if _, declared := identityForms[form]; !declared {
			t.Errorf("the data-model documents state the identity {%s} (in %v) and nothing declares it; "+
				"either it is a new form that belongs in identityForms with what it identifies, "+
				"or it is a document disagreeing with another about how an object is keyed", form, files)
		}
	}
	for form := range identityForms {
		if _, ok := seen[form]; !ok {
			t.Errorf("identityForms declares {%s} and no document states it any more; remove it", form)
		}
	}
}
