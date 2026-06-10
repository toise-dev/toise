package tenant

import (
	"strings"
	"testing"
)

// FuzzSanitize pins the safety invariants for arbitrary input: an accepted id
// is always a safe single path segment (no separators, no traversal, bounded
// length) and sanitization is idempotent.
func FuzzSanitize(f *testing.F) {
	for _, seed := range []string{"", "default", "acme", "../escape", "a/b", "a\\b",
		".", "..", "...", "ten ant", "TENANT-1_x.y", strings.Repeat("a", 100), "héllo", "\x00"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		id, ok := Sanitize(raw)
		if !ok {
			return
		}
		if id == "" {
			t.Fatalf("Sanitize(%q) accepted an empty id", raw)
		}
		if strings.ContainsAny(id, "/\\") || id == "." || id == ".." {
			t.Fatalf("Sanitize(%q) = %q: path-unsafe id accepted", raw, id)
		}
		if len(id) > 64+len(Default) {
			t.Fatalf("Sanitize(%q) = %q: unbounded id accepted", raw, id)
		}
		again, ok2 := Sanitize(id)
		if !ok2 || again != id {
			t.Fatalf("Sanitize not idempotent: %q -> %q -> (%q, %v)", raw, id, again, ok2)
		}
	})
}
