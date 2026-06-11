package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestTimeHandler_Handshake(t *testing.T) {
	s := newTestServer(t, config.Defaults())
	const t1 = int64(1700000000000000000)

	before := time.Now().UnixNano()
	req := httptest.NewRequest(http.MethodGet, "/time?t1="+strconv.FormatInt(t1, 10), nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	after := time.Now().UnixNano()

	if w.Code != 200 {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp timeResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.T1UnixNS != t1 {
		t.Errorf("t1 not echoed: got %d want %d", resp.T1UnixNS, t1)
	}
	// t2 (receive) must precede or equal t3 (transmit), and both must fall
	// inside the window the test observed the request — proves they're
	// real per-request stamps, not a cached/zero value.
	if resp.T2UnixNS > resp.T3UnixNS {
		t.Errorf("t2 (%d) must be <= t3 (%d)", resp.T2UnixNS, resp.T3UnixNS)
	}
	if resp.T2UnixNS < before || resp.T3UnixNS > after {
		t.Errorf("t2/t3 outside observed window [%d,%d]: t2=%d t3=%d", before, after, resp.T2UnixNS, resp.T3UnixNS)
	}
	// Defaults() configures a timesync server, so Offset A context rides along.
	if resp.Server == nil {
		t.Fatalf("server (Offset A) block missing")
	}
	if resp.Server.Host != "time.cloudflare.com" {
		t.Errorf("server.host = %q", resp.Server.Host)
	}
}

func TestTimeHandler_NoT1_AndProbeDisabled(t *testing.T) {
	cfg := config.Defaults()
	cfg.TimeSync.Server = "" // probe off → no Offset A block
	s := newTestServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/time", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp timeResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.T1UnixNS != 0 {
		t.Errorf("absent t1 must echo 0, got %d", resp.T1UnixNS)
	}
	if resp.T2UnixNS == 0 || resp.T3UnixNS == 0 {
		t.Errorf("t2/t3 must still be stamped without t1: t2=%d t3=%d", resp.T2UnixNS, resp.T3UnixNS)
	}
	if resp.Server != nil {
		t.Errorf("server block must be omitted when timesync.server is empty; got %+v", resp.Server)
	}
}

func TestTimeHandler_MethodNotAllowed(t *testing.T) {
	s := newTestServer(t, config.Defaults())
	req := httptest.NewRequest(http.MethodPost, "/time", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /time = %d, want 405", w.Code)
	}
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
