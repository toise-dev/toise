package version

import "testing"

func TestString(t *testing.T) {
	Version = "1.2.3"
	Commit = "abc1234"
	if got, want := String(), "1.2.3 (abc1234)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
