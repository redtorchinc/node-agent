package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/redtorchinc/node-agent/internal/buildinfo"
	"github.com/redtorchinc/node-agent/internal/netown"
)

// The /network/* surface (v0.3.0) — flow-ownership correlation for the
// gateway. Wire contract: docs/api/network-flows.md. All three routes are
// Bearer-gated (see routes()): a socket inventory with cmdlines, users
// and peer maps is recon material, so it does NOT ride the open-read LAN
// policy that /health does.

// requestSampleMaxAge throttles on-demand re-sampling from the request
// path; the background poller covers the steady state.
const requestSampleMaxAge = 2 * time.Second

// netEnvelope is the common wrapper of every /network/* response.
type netEnvelope struct {
	TsUnixNS     int64    `json:"ts_unix_ns"`
	Hostname     string   `json:"hostname"`
	AgentVersion string   `json:"agent_version"`
	Source       string   `json:"source"`
	Stale        bool     `json:"stale"`
	Partial      bool     `json:"partial"`
	Warnings     []string `json:"warnings"`
	// TrainingRunID is the active training-mode run_id, when set — the
	// backend's temporal-join key. The agent has no per-socket workflow
	// attribution (see docs/api/network-flows.md §Training context).
	TrainingRunID string `json:"training_run_id,omitempty"`
}

func (s *Server) netEnvelopeNow() netEnvelope {
	st := s.netown.Status()
	env := netEnvelope{
		TsUnixNS:     time.Now().UnixNano(),
		Hostname:     s.netown.Hostname(),
		AgentVersion: buildinfo.Version,
		Source:       st.Source,
		Stale:        st.Stale,
		Partial:      st.Partial,
		Warnings:     st.Warnings,
	}
	if t := s.modeMgr.Training(); t != nil {
		env.TrainingRunID = t.RunID
	}
	return env
}

type socketsResp struct {
	netEnvelope
	Items []netown.SocketItem `json:"items"`
}

func (s *Server) handleNetworkSockets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	f := netown.SocketFilter{
		State: q.Get("state"),
		Proto: q.Get("proto"),
	}
	if !validProto(f.Proto) {
		http.Error(w, "proto must be tcp or udp", http.StatusBadRequest)
		return
	}
	var ok bool
	if f.Port, ok = parsePort(q.Get("port")); !ok {
		http.Error(w, "bad port", http.StatusBadRequest)
		return
	}
	if f.PID, ok = parsePID(q.Get("pid")); !ok {
		http.Error(w, "bad pid", http.StatusBadRequest)
		return
	}
	f.Limit = parseLimit(q.Get("limit"))

	s.netown.SampleIfOlder(requestSampleMaxAge)
	items := s.netown.Sockets(f)
	if items == nil {
		items = []netown.SocketItem{}
	}
	writeJSON(w, http.StatusOK, socketsResp{netEnvelope: s.netEnvelopeNow(), Items: items})
}

type flowsResp struct {
	netEnvelope
	WindowS int              `json:"window_s"`
	Items   []netown.FlowItem `json:"items"`
}

func (s *Server) handleNetworkFlows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	f := netown.FlowFilter{
		Proto:      q.Get("proto"),
		RemoteAddr: q.Get("remote_addr"),
	}
	if !validProto(f.Proto) {
		http.Error(w, "proto must be tcp or udp", http.StatusBadRequest)
		return
	}
	var ok bool
	if f.LocalPort, ok = parsePort(q.Get("local_port")); !ok {
		http.Error(w, "bad local_port", http.StatusBadRequest)
		return
	}
	if f.PID, ok = parsePID(q.Get("pid")); !ok {
		http.Error(w, "bad pid", http.StatusBadRequest)
		return
	}
	if v := q.Get("since_unix_ns"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			http.Error(w, "bad since_unix_ns", http.StatusBadRequest)
			return
		}
		f.SinceNS = n
	}
	f.Limit = parseLimit(q.Get("limit"))

	s.netown.SampleIfOlder(requestSampleMaxAge)
	items := s.netown.Flows(f)
	if items == nil {
		items = []netown.FlowItem{}
	}
	writeJSON(w, http.StatusOK, flowsResp{
		netEnvelope: s.netEnvelopeNow(),
		WindowS:     s.netown.WindowS(),
		Items:       items,
	})
}

type resolveResp struct {
	netEnvelope
	Query netown.Query `json:"query"`
	Match netown.Match `json:"match"`
}

func (s *Server) handleNetworkResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	query := netown.Query{
		Proto:      q.Get("proto"),
		LocalAddr:  q.Get("local_addr"),
		RemoteAddr: q.Get("remote_addr"),
	}
	if query.Proto != "tcp" && query.Proto != "udp" {
		http.Error(w, "proto required (tcp or udp)", http.StatusBadRequest)
		return
	}
	if query.LocalAddr == "" || query.RemoteAddr == "" {
		http.Error(w, "local_addr and remote_addr required", http.StatusBadRequest)
		return
	}
	var ok bool
	if query.LocalPort, ok = parsePort(q.Get("local_port")); !ok || query.LocalPort == 0 {
		http.Error(w, "local_port required", http.StatusBadRequest)
		return
	}
	if query.RemotePort, ok = parsePort(q.Get("remote_port")); !ok || query.RemotePort == 0 {
		http.Error(w, "remote_port required", http.StatusBadRequest)
		return
	}
	if v := q.Get("observed_at_unix_ns"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			http.Error(w, "bad observed_at_unix_ns", http.StatusBadRequest)
			return
		}
		query.ObservedAtNS = n
	}

	s.netown.SampleIfOlder(requestSampleMaxAge)
	writeJSON(w, http.StatusOK, resolveResp{
		netEnvelope: s.netEnvelopeNow(),
		Query:       query,
		Match:       s.netown.Resolve(query),
	})
}

func validProto(p string) bool { return p == "" || p == "tcp" || p == "udp" }

// parsePort parses an optional port param. ("", true) → 0 = unset.
func parsePort(v string) (uint32, bool) {
	if v == "" {
		return 0, true
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil || n > 65535 {
		return 0, false
	}
	return uint32(n), true
}

func parsePID(v string) (int32, bool) {
	if v == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < 0 {
		return 0, false
	}
	return int32(n), true
}

// parseLimit clamps ?limit to [1,10000], default 1000. Malformed values
// fall back to the default rather than erroring — a filter, not a contract.
func parseLimit(v string) int {
	const def, max = 1000, 10000
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}
