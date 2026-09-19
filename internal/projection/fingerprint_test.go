package projection

import (
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

// The fingerprint is what two nodes agree on without talking (ADR 0035): the
// same identity observed anywhere computes the same value, where the logical id
// is minted locally and differs per node.
func TestResolveFingerprintIsNodeIndependent(t *testing.T) {
	ident := []model.KeyValue{kv("host.id", "h1")}

	nodeA, idA := New(), model.NewEntityID()
	nodeA.Apply(entityCreated(idA, model.TypeHost, ident...))

	nodeB, idB := New(), model.NewEntityID()
	nodeB.Apply(entityCreated(idB, model.TypeHost, ident...))

	if idA == idB {
		t.Fatal("two nodes minted the same logical id; the test proves nothing")
	}

	fp := model.Entity{Type: model.TypeHost, Identity: ident}.IdentityHash()

	gotA, okA := nodeA.ResolveFingerprint(fp)
	gotB, okB := nodeB.ResolveFingerprint(fp)
	if !okA || !okB {
		t.Fatalf("fingerprint did not resolve on both nodes: A=%v B=%v", okA, okB)
	}
	if gotA != idA || gotB != idB {
		t.Errorf("resolved to the wrong ids: A=%q want %q, B=%q want %q", gotA, idA, gotB, idB)
	}
}

// A handle reaches exactly what its logical id reaches. A soft-deleted entity
// stays readable by id until its tombstone is pruned, so the fingerprint must
// reach it too — otherwise the two handles disagree about what exists.
func TestResolveFingerprintReachesSoftDeleted(t *testing.T) {
	g := New()
	id := model.NewEntityID()
	ident := []model.KeyValue{kv("host.id", "h1")}
	g.Apply(entityCreated(id, model.TypeHost, ident...))

	fp := model.Entity{Type: model.TypeHost, Identity: ident}.IdentityHash()
	g.Apply(model.Event{Entity: &model.EntityEvent{
		ChangeType: model.EntityDeleted,
		Entity:     model.Entity{ID: id, Type: model.TypeHost, Identity: ident},
		RecordedAt: time.Now(),
	}})

	if _, ok, del := g.GetEntity(id); !ok || !del {
		t.Fatalf("precondition: entity should be readable by id and soft-deleted (ok=%v del=%v)", ok, del)
	}
	got, ok := g.ResolveFingerprint(fp)
	if !ok || got != id {
		t.Errorf("ResolveFingerprint after delete = %q, %v; want %q, true", got, ok, id)
	}
}

func TestResolveFingerprintUnknown(t *testing.T) {
	g := New()
	g.Apply(entityCreated(model.NewEntityID(), model.TypeHost, kv("host.id", "h1")))

	if got, ok := g.ResolveFingerprint("host:deadbeef"); ok {
		t.Errorf("unknown fingerprint resolved to %q", got)
	}
}

// One argument accepts both handles because the namespaces cannot collide: a
// fingerprint carries a colon, a ULID cannot.
func TestResolveHandleAcceptsBoth(t *testing.T) {
	g := New()
	id := model.NewEntityID()
	ident := []model.KeyValue{kv("host.id", "h1")}
	g.Apply(entityCreated(id, model.TypeHost, ident...))
	fp := model.Entity{Type: model.TypeHost, Identity: ident}.IdentityHash()

	if got, ok := g.ResolveHandle(fp); !ok || got != id {
		t.Errorf("ResolveHandle(fingerprint) = %q, %v; want %q, true", got, ok, id)
	}
	if got, ok := g.ResolveHandle(string(id)); !ok || got != id {
		t.Errorf("ResolveHandle(id) = %q, %v; want %q, true", got, ok, id)
	}

	// An unknown id passes through: whether it exists is the caller's lookup,
	// exactly as before ResolveHandle existed.
	other := model.NewEntityID()
	if got, ok := g.ResolveHandle(string(other)); !ok || got != other {
		t.Errorf("ResolveHandle(unknown id) = %q, %v; want %q, true", got, ok, other)
	}

	// A malformed fingerprint fails loudly rather than being mistaken for an id.
	if _, ok := g.ResolveHandle("host:not-a-real-hash"); ok {
		t.Error("a malformed fingerprint resolved")
	}
}

func TestIdentityFingerprintHasNoULIDCollision(t *testing.T) {
	fp := model.Entity{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}}.IdentityHash()
	for _, c := range string(model.NewEntityID()) {
		if c == ':' {
			t.Fatal("a ULID contains a colon; the two handle namespaces would collide")
		}
	}
	if !containsColon(fp) {
		t.Fatalf("fingerprint %q carries no colon; the two handle namespaces would collide", fp)
	}
}

func containsColon(s string) bool {
	for _, c := range s {
		if c == ':' {
			return true
		}
	}
	return false
}
