package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"

	"github.com/toise-dev/toise/internal/model"
)

// TestOpenReadOnlyNeverWrites pins the no-write contract of OpenReadOnly
// (#162): a missing dir is refused without being created, a pre-marker store
// is read without being stamped, and appends fail.
func TestOpenReadOnlyNeverWrites(t *testing.T) {
	t.Run("missing dir refused and not created", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "absent")
		if _, err := OpenReadOnly(dir, DefaultConfig()); err == nil {
			t.Fatal("want an error opening a missing dir read-only, got nil")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("read-only open created %s (stat err = %v)", dir, err)
		}
	})

	t.Run("pre-marker store is not stamped", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir, DefaultConfig())
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if aerr := s.Append(mkEntityEvent("e1", model.EntityCreated, ts(0))); aerr != nil {
			t.Fatalf("append: %v", aerr)
		}
		if cerr := s.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		stripFormatMarker(t, dir)

		ro, err := OpenReadOnly(dir, DefaultConfig())
		if err != nil {
			t.Fatalf("read-only open of a pre-marker store: %v", err)
		}
		if got := scanAll(t, ro); len(got) != 1 {
			t.Errorf("scanned %d events, want 1", len(got))
		}
		if aerr := ro.Append(mkEntityEvent("e2", model.EntityCreated, ts(1))); aerr == nil {
			t.Error("append on a read-only store must fail")
		}
		if cerr := ro.Close(); cerr != nil {
			t.Fatal(cerr)
		}

		db, err := pebble.Open(dir, &pebble.Options{ReadOnly: true})
		if err != nil {
			t.Fatalf("reopen pebble: %v", err)
		}
		defer func() { _ = db.Close() }()
		if _, closer, gerr := db.Get(metaFormatKey); !errors.Is(gerr, pebble.ErrNotFound) {
			if gerr == nil {
				_ = closer.Close()
			}
			t.Errorf("format marker after read-only open: err = %v, want ErrNotFound", gerr)
		}
	})

	t.Run("newer format still refused", func(t *testing.T) {
		dir := t.TempDir()
		db, err := pebble.Open(dir, &pebble.Options{})
		if err != nil {
			t.Fatalf("pebble open: %v", err)
		}
		if serr := db.Set(metaFormatKey, encodeU64(formatVersion+1), pebble.Sync); serr != nil {
			t.Fatal(serr)
		}
		if cerr := db.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		_, err = OpenReadOnly(dir, DefaultConfig())
		if err == nil || !strings.Contains(err.Error(), "newer") {
			t.Errorf("opening a newer-format store read-only: err = %v, want a format refusal", err)
		}
	})
}

// TestOpenReadOnlyCheckpoint proves a read-only open still supports the one
// thing the backup path needs: a complete, replayable Pebble checkpoint.
func TestOpenReadOnlyCheckpoint(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if aerr := s.Append(mkEntityEvent("e1", model.EntityCreated, ts(0))); aerr != nil {
		t.Fatalf("append: %v", aerr)
	}
	if cerr := s.Close(); cerr != nil {
		t.Fatal(cerr)
	}

	ro, err := OpenReadOnly(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("read-only open: %v", err)
	}
	defer func() { _ = ro.Close() }()
	dst := filepath.Join(t.TempDir(), "ckpt")
	if cerr := ro.Checkpoint(dst); cerr != nil {
		t.Fatalf("checkpoint from a read-only store: %v", cerr)
	}

	restored, err := Open(dst, DefaultConfig())
	if err != nil {
		t.Fatalf("open checkpoint: %v", err)
	}
	defer func() { _ = restored.Close() }()
	if got := scanAll(t, restored); len(got) != 1 {
		t.Errorf("checkpoint replays %d events, want 1", len(got))
	}
}

// stripFormatMarker rewrites dir's store without its format marker, simulating
// a store written before the marker existed.
func stripFormatMarker(t *testing.T, dir string) {
	t.Helper()
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble open: %v", err)
	}
	if derr := db.Delete(metaFormatKey, pebble.Sync); derr != nil {
		t.Fatal(derr)
	}
	if cerr := db.Close(); cerr != nil {
		t.Fatal(cerr)
	}
}
