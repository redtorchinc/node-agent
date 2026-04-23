package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/redtorchinc/node-agent/internal/config"
	"github.com/redtorchinc/node-agent/internal/health"
)

func newTestServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	r, err := health.NewReporter(cfg)
	if err != nil {
		t.Fatalf("NewReporter: %v", err)
	}
	return New(cfg, r)
}

func TestVersionHandler(t *testing.T) {
	s := newTestServer(t, config.Defaults())
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["version"]; !ok {
		t.Errorf("missing version in %v", body)
	}
}

func TestHealthHandler_JSONShape(t *testing.T) {
	s := newTestServer(t, config.Defaults())
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	// Confirm every SPEC top-level key is present.
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, k := range []string{
		"ts", "hostname", "os", "arch", "agent_version", "uptime_s",
		"cpu", "memory", "gpus", "service_allocators", "ollama",
		"degraded", "degraded_reasons",
	} {
		if _, ok := body[k]; !ok {
			t.Errorf("missing %q in /health", k)
		}
	}
}

func TestUnload_NoToken_503(t *testing.T) {
	s := newTestServer(t, config.Defaults())
	req := httptest.NewRequest(http.MethodPost, "/actions/unload-model",
		strings.NewReader(`{"model":"x"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 when no token configured, got %d", w.Code)
	}
}

func TestUnload_BadToken_401(t *testing.T) {
	cfg := config.Defaults()
	cfg.Token = "expected"
	s := newTestServer(t, cfg)
	req := httptest.NewRequest(http.MethodPost, "/actions/unload-model",
		strings.NewReader(`{"model":"x"}`))
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestUnload_MissingAuth_401(t *testing.T) {
	cfg := config.Defaults()
	cfg.Token = "expected"
	s := newTestServer(t, cfg)
	req := httptest.NewRequest(http.MethodPost, "/actions/unload-model",
		strings.NewReader(`{"model":"x"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}
