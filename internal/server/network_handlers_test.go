package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/redtorchinc/node-agent/internal/config"
	"github.com/redtorchinc/node-agent/internal/netown"
)

// Deterministic netown deps so handler tests never touch the real socket
// table (which would be nondeterministic and need privileges on CI).
type stubSampler struct{ conns []netown.RawConn }

func (s stubSampler) Sample() ([]netown.RawConn, error) { return s.conns, nil }
func (s stubSampler) Source() string                    { return "stub" }

type stubProcs struct{}

func (stubProcs) Info(pid int32) (netown.ProcInfo, error) {
	return netown.ProcInfo{
		Name:       "svc-example",
		Exe:        "/usr/local/bin/svc-example",
		User:       "svc",
		CmdlineRaw: []string{"svc-example", "--token", "sk-test-secret", "--listen", ":9000"},
		Service:    "rt-vllm-example.service",
	}, nil
}

func newNetTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.Defaults()
	cfg.Token = "test-token"
	s := newTestServer(t, cfg)
	s.netown = netown.NewWithDeps(netown.Config{}, stubSampler{conns: []netown.RawConn{
		{Proto: "tcp", LocalAddr: "0.0.0.0", LocalPort: 9000, State: "listen", PID: 42},
		{Proto: "tcp", LocalAddr: "198.51.100.10", LocalPort: 9000, RemoteAddr: "198.51.100.20", RemotePort: 50123, State: "established", PID: 42},
	}}, stubProcs{})
	return s
}

func netGet(s *Server, path string, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func TestNetworkEndpoints_RequireBearer(t *testing.T) {
	s := newNetTestServer(t)
	for _, p := range []string{"/network/sockets", "/network/flows", "/network/resolve"} {
		if w := netGet(s, p, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without token = %d, want 401", p, w.Code)
		}
		if w := netGet(s, p+"?x=1", "wrong"); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s bad token = %d, want 401", p, w.Code)
		}
	}
}

func TestNetworkSockets_EnvelopeAndRedaction(t *testing.T) {
	s := newNetTestServer(t)
	w := netGet(s, "/network/sockets", "test-token")
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		TsUnixNS     int64                `json:"ts_unix_ns"`
		Hostname     string               `json:"hostname"`
		AgentVersion string               `json:"agent_version"`
		Source       string               `json:"source"`
		Warnings     []string             `json:"warnings"`
		Items        []netown.SocketItem  `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TsUnixNS == 0 || resp.Hostname == "" || resp.Source != "stub" {
		t.Errorf("envelope: %+v", resp)
	}
	if resp.Warnings == nil {
		t.Error("warnings must be [], not null")
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items: %+v", resp.Items)
	}
	for _, it := range resp.Items {
		if it.Service != "rt-vllm-example.service" {
			t.Errorf("service: %+v", it)
		}
		if it.CmdlineHead == "" || strings.Contains(it.CmdlineHead, "sk-test-secret") {
			t.Errorf("cmdline_head must be present and redacted: %q", it.CmdlineHead)
		}
	}
}

func TestNetworkSockets_BadParams(t *testing.T) {
	s := newNetTestServer(t)
	for _, q := range []string{"?proto=icmp", "?port=99999", "?pid=abc"} {
		if w := netGet(s, "/network/sockets"+q, "test-token"); w.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", q, w.Code)
		}
	}
}

func TestNetworkFlows_Window(t *testing.T) {
	s := newNetTestServer(t)
	w := netGet(s, "/network/flows?proto=tcp&local_port=9000", "test-token")
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		WindowS int                `json:"window_s"`
		Items   []netown.FlowItem  `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.WindowS != 300 {
		t.Errorf("window_s = %d", resp.WindowS)
	}
	if len(resp.Items) != 2 {
		t.Errorf("items: %+v", resp.Items)
	}
	for _, it := range resp.Items {
		if it.FlowID == "" || !it.Live {
			t.Errorf("flow item: %+v", it)
		}
	}
}

func TestNetworkResolve_HappyAndValidation(t *testing.T) {
	s := newNetTestServer(t)

	w := netGet(s, "/network/resolve?proto=tcp&local_addr=198.51.100.10&local_port=9000&remote_addr=198.51.100.20&remote_port=50123", "test-token")
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Query netown.Query `json:"query"`
		Match netown.Match `json:"match"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Match.Status != "matched" || resp.Match.Owner == nil || resp.Match.Owner.PID != 42 {
		t.Errorf("match: %+v", resp.Match)
	}

	// not_found is 200, not 404.
	w = netGet(s, "/network/resolve?proto=udp&local_addr=198.51.100.10&local_port=53&remote_addr=203.0.113.9&remote_port=53", "test-token")
	if w.Code != 200 {
		t.Fatalf("not_found must be 200, got %d", w.Code)
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Match.Status != "not_found" {
		t.Errorf("match: %+v", resp.Match)
	}

	// Missing required params → 400.
	for _, q := range []string{
		"?proto=tcp&local_addr=1.2.3.4&local_port=1&remote_addr=5.6.7.8", // no remote_port
		"?local_addr=1.2.3.4&local_port=1&remote_addr=5.6.7.8&remote_port=2", // no proto
		"?proto=tcp&local_port=1&remote_addr=5.6.7.8&remote_port=2",      // no local_addr
	} {
		if w := netGet(s, "/network/resolve"+q, "test-token"); w.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", q, w.Code)
		}
	}
}

func TestNetworkDisabled_RoutesAbsentAndCapabilityFalse(t *testing.T) {
	cfg := config.Defaults()
	cfg.Token = "test-token"
	cfg.Network.FlowsEnabled = "false"
	s := newTestServer(t, cfg)

	if w := netGet(s, "/network/sockets", "test-token"); w.Code != http.StatusNotFound {
		t.Errorf("disabled route = %d, want 404", w.Code)
	}
	w := netGet(s, "/capabilities", "")
	var caps Capabilities
	if err := json.Unmarshal(w.Body.Bytes(), &caps); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if caps.NetworkFlowsSupported {
		t.Error("network_flows_supported must be false when disabled")
	}
}

func TestNetworkEnabled_CapabilityTrue(t *testing.T) {
	s := newNetTestServer(t)
	w := netGet(s, "/capabilities", "")
	var caps Capabilities
	if err := json.Unmarshal(w.Body.Bytes(), &caps); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !caps.NetworkFlowsSupported {
		t.Error("network_flows_supported must be true by default")
	}
}
