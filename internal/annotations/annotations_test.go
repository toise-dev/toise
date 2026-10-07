package annotations

import (
	"testing"
	"time"
)

func TestSetMergeGetDelete(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Unix(1_700_000_000, 0).UTC()

	_, ok, err := s.Get("e1")
	if err != nil || ok {
		t.Fatalf("absent annotation: ok=%v err=%v", ok, err)
	}

	if _, err = s.Set("e1", map[string]string{"owner": "sre", "ticket": "T-1"}, "alice", now); err != nil {
		t.Fatal(err)
	}
	a, ok, err := s.Get("e1")
	if err != nil || !ok {
		t.Fatalf("get after set: ok=%v err=%v", ok, err)
	}
	if a.Values["owner"] != "sre" || a.Values["ticket"] != "T-1" || a.Author != "alice" || !a.UpdatedAt.Equal(now) {
		t.Fatalf("unexpected annotation: %+v", a)
	}

	// merge: add a key, keep the others
	if _, err := s.Set("e1", map[string]string{"note": "draining"}, "bob", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	a, _, _ = s.Get("e1")
	if len(a.Values) != 3 || a.Values["note"] != "draining" || a.Author != "bob" {
		t.Fatalf("merge wrong: %+v", a)
	}

	// empty value removes a key
	if _, err := s.Set("e1", map[string]string{"ticket": ""}, "bob", now); err != nil {
		t.Fatal(err)
	}
	a, _, _ = s.Get("e1")
	if _, present := a.Values["ticket"]; present || len(a.Values) != 2 {
		t.Fatalf("empty value should remove the key: %+v", a)
	}

	// clearing every key removes the row
	if _, err := s.Set("e1", map[string]string{"owner": "", "note": ""}, "bob", now); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get("e1"); ok {
		t.Error("annotation with no values left should be gone")
	}

	// explicit delete
	_, _ = s.Set("e2", map[string]string{"k": "v"}, "x", now)
	if err := s.Delete("e2"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get("e2"); ok {
		t.Error("e2 should be deleted")
	}
}

// TestCountMakesTheOverlayMeasurable is the measurable half of #365. The
// annotation overlay is replica-local: it lives in a sidecar that log shipping
// does not replicate, so a pair holds notes on whichever replica received the
// write and on no other. Found on a production pair: two annotated entities on
// one replica, zero on the standby — and the notes in question were that pair's
// own reboot constraint, the thing an operator reads precisely when the primary
// is the one being worked on.
//
// Counting does not replicate anything. It makes the divergence alertable,
// which is what can be done without first choosing how to replicate.
func TestCountMakesTheOverlayMeasurable(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if n, err := s.Count(); err != nil || n != 0 {
		t.Fatalf("empty store: Count = %d, %v; want 0, nil", n, err)
	}

	if _, err := s.Set("01ENT_A", map[string]string{"ha.pair": "sha001/sha002"}, "ops", time.Now()); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := s.Set("01ENT_B", map[string]string{"ops.reboot.constraint": "never both at once"}, "ops", time.Now()); err != nil {
		t.Fatalf("set: %v", err)
	}
	if n, err := s.Count(); err != nil || n != 2 {
		t.Fatalf("two annotated entities: Count = %d, %v; want 2, nil", n, err)
	}

	// Merging onto an existing entity does not inflate the count: the metric
	// counts annotated ENTITIES, which is what two replicas must agree on.
	if _, err := s.Set("01ENT_A", map[string]string{"owner": "sre"}, "ops", time.Now()); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if n, _ := s.Count(); n != 2 {
		t.Errorf("after merging onto an existing entity: Count = %d, want 2", n)
	}
}
