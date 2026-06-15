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
