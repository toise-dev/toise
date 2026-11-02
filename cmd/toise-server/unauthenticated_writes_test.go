package main

import (
	"testing"

	"github.com/toise-dev/toise/internal/config"
)

// TestUnauthenticatedWriteIsAnAlertableExposure pins that an anonymous writer
// reaches the gauge rather than only a boot log. The project already learned
// this the expensive way: three true conditions sat in a production log for
// months because a WARN wakes nobody.
//
// The condition exists beside no_auth rather than inside it because the two
// answer different questions. no_auth fires on a listener with no credential
// path at all; this one fires when an anonymous caller may MUTATE the graph,
// which is the part that was open for four releases.
func TestUnauthenticatedWriteIsAnAlertableExposure(t *testing.T) {
	has := func(cfg config.Config, authEnabled bool) bool {
		for _, c := range insecureConditions(cfg, authEnabled) {
			if c.Surface == "write" && c.Reason == "unauthenticated_writes" {
				return true
			}
		}
		return false
	}

	if !has(config.Config{Listen: "0.0.0.0:8080", AllowUnauthenticatedWrites: true}, false) {
		t.Error("an anonymous writer on an exposed listener must raise the condition")
	}
	if has(config.Config{Listen: "127.0.0.1:8080", AllowUnauthenticatedWrites: true}, false) {
		t.Error("a loopback listener is not an exposure")
	}
	if has(config.Config{Listen: "0.0.0.0:8080"}, false) {
		t.Error("writes refused: no condition")
	}
	// With authentication on, the knob grants nothing: an anonymous caller never
	// reaches a write handler, so there is no exposure to report. This is also
	// what keeps the gauge honest — it tracks the effective posture, not a
	// setting, and it starts firing the moment authentication goes away.
	if has(config.Config{Listen: "0.0.0.0:8080", AllowUnauthenticatedWrites: true}, true) {
		t.Error("with authentication enabled the knob grants nothing, so no condition")
	}
}
