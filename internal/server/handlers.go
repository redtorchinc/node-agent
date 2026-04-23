package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/redtorchinc/node-agent/internal/buildinfo"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rep, err := s.reporter.Report(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("build report: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"version":    buildinfo.Version,
		"git_sha":    buildinfo.GitSHA,
		"build_time": buildinfo.BuildTime,
	})
}

type unloadReq struct {
	Model string `json:"model"`
}

type unloadResp struct {
	Status   string   `json:"status"`
	Unloaded []string `json:"unloaded"`
	TookMS   int64    `json:"took_ms"`
}

func (s *Server) handleUnload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req unloadReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		http.Error(w, "missing model", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	start := time.Now()
	res, err := s.ollama.Unload(ctx, req.Model)
	took := time.Since(start).Milliseconds()
	if err != nil {
		slog.Warn("unload failed", "model", req.Model, "err", err, "remote", r.RemoteAddr)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("unload ok",
		"model", req.Model,
		"unloaded", res.Unloaded,
		"took_ms", took,
		"remote", r.RemoteAddr,
	)
	writeJSON(w, http.StatusOK, unloadResp{
		Status:   "ok",
		Unloaded: res.Unloaded,
		TookMS:   took,
	})
}

// handleMetrics emits a minimal Prometheus text-format view of /health.
// Scope: just enough to plot from Grafana if operators ask for it. Behind
// RT_AGENT_METRICS=1; labels intentionally minimal.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rep, err := s.reporter.Report(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# HELP rt_agent_memory_used_pct Host RAM used percent\n")
	fmt.Fprintf(w, "# TYPE rt_agent_memory_used_pct gauge\n")
	fmt.Fprintf(w, "rt_agent_memory_used_pct %f\n", rep.Memory.UsedPct)
	fmt.Fprintf(w, "# HELP rt_agent_swap_used_pct Host swap used percent\n")
	fmt.Fprintf(w, "# TYPE rt_agent_swap_used_pct gauge\n")
	fmt.Fprintf(w, "rt_agent_swap_used_pct %f\n", rep.Memory.SwapUsedPct)
	fmt.Fprintf(w, "# HELP rt_agent_degraded 1 if any hard degraded_reasons present\n")
	fmt.Fprintf(w, "# TYPE rt_agent_degraded gauge\n")
	d := 0
	if rep.Degraded {
		d = 1
	}
	fmt.Fprintf(w, "rt_agent_degraded %d\n", d)
	for _, g := range rep.GPUs {
		fmt.Fprintf(w, "rt_agent_gpu_vram_used_pct{index=\"%d\",name=%q} %f\n",
			g.Index, g.Name, g.VRAMUsedPct)
		fmt.Fprintf(w, "rt_agent_gpu_util_pct{index=\"%d\"} %d\n", g.Index, g.UtilPct)
	}
	fmt.Fprintf(w, "rt_agent_ollama_up %d\n", boolI(rep.Ollama.Up))
	for _, a := range rep.ServiceAllocs {
		fmt.Fprintf(w, "rt_agent_service_reserved_mb{name=%q} %f\n", a.Name, a.ReservedMB)
		fmt.Fprintf(w, "rt_agent_service_allocated_mb{name=%q} %f\n", a.Name, a.AllocatedMB)
	}
}

func boolI(b bool) int {
	if b {
		return 1
	}
	return 0
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
