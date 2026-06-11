package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toise-dev/toise/internal/store"
)

// TestOpenExistingNeverMints pins #162: the read-only open path must refuse
// to create anything — a missing data dir, an empty one, and an unmigrated
// legacy layout are errors, never a freshly minted (empty) store.
func TestOpenExistingNeverMints(t *testing.T) {
	t.Run("missing data dir", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "absent")
		if _, err := OpenExisting(dir, store.DefaultConfig(), discard()); err == nil {
			t.Fatal("want an error for a missing data dir, got nil")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("OpenExisting created %s (stat err = %v)", dir, err)
		}
	})

	t.Run("empty data dir", func(t *testing.T) {
		dir := t.TempDir()
		_, err := OpenExisting(dir, store.DefaultConfig(), discard())
		if err == nil || !strings.Contains(err.Error(), "no tenant stores") {
			t.Fatalf("err = %v, want a no-tenant-stores refusal", err)
		}
		ents, rerr := os.ReadDir(dir)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if len(ents) != 0 {
			t.Errorf("OpenExisting left %d entries in an empty data dir", len(ents))
		}
	})

	t.Run("legacy layout refused unmigrated", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, legacyMarker), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := OpenExisting(dir, store.DefaultConfig(), discard())
		if err == nil || !strings.Contains(err.Error(), "legacy") {
			t.Fatalf("err = %v, want a legacy-layout refusal", err)
		}
		if _, serr := os.Stat(filepath.Join(dir, legacyMarker)); serr != nil {
			t.Errorf("legacy marker gone after OpenExisting: %v", serr)
		}
	})
}

// TestOpenExistingOpensOnlyPersistedTenants pins that the read-only path
// serves exactly what is on disk: no default tenant appears, and the source
// directory is byte-identical afterwards.
func TestOpenExistingOpensOnlyPersistedTenants(t *testing.T) {
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "acme"), store.DefaultConfig())
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	before := listTree(t, dataDir)

	stores, err := OpenExisting(dataDir, store.DefaultConfig(), discard())
	if err != nil {
		t.Fatalf("OpenExisting: %v", err)
	}
	if len(stores) != 1 || stores[0].Tenant != "acme" {
		t.Fatalf("opened %d stores, want exactly [acme]", len(stores))
	}
	for _, ts := range stores {
		if cerr := ts.Store.Close(); cerr != nil {
			t.Fatal(cerr)
		}
	}

	after := listTree(t, dataDir)
	if before != after {
		t.Errorf("data dir changed across a read-only open:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// listTree returns a newline-joined recursive listing of dir with file sizes,
// so a test can assert a directory was not touched.
func listTree(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		if info.IsDir() {
			fmt.Fprintf(&b, "%s/\n", rel)
		} else {
			fmt.Fprintf(&b, "%s %d\n", rel, info.Size())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return b.String()
}
