package version

import "testing"

// keep restores the build variables a test overwrites, so no test leaks its
// values into another.
func keep(t *testing.T) {
	t.Helper()
	v, c, b := Version, Commit, Built
	t.Cleanup(func() { Version, Commit, Built = v, c, b })
}

func TestString(t *testing.T) {
	keep(t)
	Version = "1.2.3"
	Commit = "abc1234567890"
	if got, want := String(), "1.2.3 (abc1234)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestStringShortCommit(t *testing.T) {
	keep(t)
	Version = "1.2.3"
	Commit = "abc123"
	if got, want := String(), "1.2.3 (abc123)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestLine(t *testing.T) {
	keep(t)
	Version = "v0.24.3"
	Commit = "ff7955ee186cceac8da7dadca58a975749b819f5"
	Built = "2026-11-13T10:42:00Z"
	want := "v0.24.3 ff7955ee186cceac8da7dadca58a975749b819f5 2026-11-13T10:42:00Z"
	if got := Line(); got != want {
		t.Errorf("Line() = %q, want %q", got, want)
	}
}

func TestGet(t *testing.T) {
	keep(t)
	Version = "v0.24.3"
	Commit = "ff7955ee186cceac8da7dadca58a975749b819f5"
	Built = "2026-11-13T10:42:00Z"
	want := Info{
		Version: "v0.24.3",
		Commit:  "ff7955ee186cceac8da7dadca58a975749b819f5",
		Built:   "2026-11-13T10:42:00Z",
	}
	if got := Get(); got != want {
		t.Errorf("Get() = %+v, want %+v", got, want)
	}
}
