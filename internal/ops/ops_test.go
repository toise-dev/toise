package ops

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	Healthz()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("body = %q, want it to contain ok", rec.Body.String())
	}
}

func TestReadyzReady(t *testing.T) {
	rec := httptest.NewRecorder()
	Readyz(func() error { return nil })(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestReadyzNotReady(t *testing.T) {
	rec := httptest.NewRecorder()
	Readyz(func() error { return errors.New("store down") })(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "store down") {
		t.Errorf("body = %q, want the reason surfaced", rec.Body.String())
	}
}
