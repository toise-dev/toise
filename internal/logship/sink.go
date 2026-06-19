// Package logship ships immutable event-log segments off-node on a cadence —
// the continuous, finer-RPO complement to the cold checkpoint backup (ADR 0029).
// A tenant's log is exported as contiguous segments keyed by sequence range; the
// shipper derives its cursor from the sink itself, so it is stateless and
// crash-safe. The Sink abstraction keeps any object-store SDK out of the core
// build (ADR 0030): the filesystem sink here serves NFS / a mounted bucket /
// rsync targets, and an S3-class Sink is a drop-in.
package logship

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Sink is a content-addressable store for immutable segment objects. Names are
// forward-slash paths (e.g. "acme/0000...-0000....seg"); a backend maps them to
// its own layout. Put must be atomic (no torn object is ever observable).
type Sink interface {
	Put(ctx context.Context, name string, data []byte) error
	Get(ctx context.Context, name string) ([]byte, error)
	// List returns the names under prefix, in lexical order.
	List(ctx context.Context, prefix string) ([]string, error)
}

// FileSink is a Sink backed by a local directory tree. Point it at a mounted
// object-store bucket, an NFS export, or an rsync staging dir. It has no
// external dependency, so it ships in the core build (ADR 0030).
type FileSink struct{ root string }

// NewFileSink roots a FileSink at dir, creating it if absent.
func NewFileSink(dir string) (*FileSink, error) {
	if dir == "" {
		return nil, fmt.Errorf("logship: empty sink directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("logship: creating sink dir %s: %w", dir, err)
	}
	return &FileSink{root: dir}, nil
}

func (f *FileSink) path(name string) string {
	return filepath.Join(f.root, filepath.FromSlash(name))
}

// Put writes name atomically: a temp file in the destination directory, fsynced,
// then renamed — so a reader never sees a partial segment and a crash leaves no
// half-written object behind.
func (f *FileSink) Put(ctx context.Context, name string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dst := f.path(name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("logship: mkdir for %s: %w", name, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".seg-*")
	if err != nil {
		return fmt.Errorf("logship: temp for %s: %w", name, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("logship: write %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("logship: sync %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("logship: close %s: %w", name, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("logship: publish %s: %w", name, err)
	}
	return nil
}

// Get reads an object previously written by Put.
func (f *FileSink) Get(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(f.path(name))
	if err != nil {
		return nil, fmt.Errorf("logship: reading %s: %w", name, err)
	}
	return data, nil
}

// List returns segment names under prefix in lexical order, skipping the
// in-flight temp files Put leaves until rename.
func (f *FileSink) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var names []string
	walkRoot := f.root
	err := filepath.WalkDir(walkRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(walkRoot, p)
		if rerr != nil {
			return rerr
		}
		name := filepath.ToSlash(rel)
		if strings.HasPrefix(filepath.Base(name), ".seg-") {
			return nil // an in-flight temp file
		}
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("logship: listing %q: %w", prefix, err)
	}
	sort.Strings(names)
	return names, nil
}
