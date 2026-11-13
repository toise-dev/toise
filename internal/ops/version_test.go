package ops

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/toise-dev/toise/internal/version"
)

func TestVersion(t *testing.T) {
	v, c, b := version.Version, version.Commit, version.Built
	t.Cleanup(func() { version.Version, version.Commit, version.Built = v, c, b })
	version.Version = "v0.24.3"
	version.Commit = "ff7955ee186cceac8da7dadca58a975749b819f5"
	version.Built = "2026-11-13T10:42:00Z"

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	Version()(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
	var got version.Info
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	want := version.Info{
		Version: "v0.24.3",
		Commit:  "ff7955ee186cceac8da7dadca58a975749b819f5",
		Built:   "2026-11-13T10:42:00Z",
	}
	if got != want {
		t.Errorf("body = %+v, want %+v", got, want)
	}
}
