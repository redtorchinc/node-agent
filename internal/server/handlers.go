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

// handleMetrics emits a Prometheus text-format view of /health. Behind
// RT_AGENT_METRICS=1. Label cardinality is bounded by node-local concepts
// (GPU index, platform name, model name, RDMA device) — no unbounded
// labels are emitted.
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
	// Host-level gauges (v0.1 contract kept verbatim).
	fmt.Fprintf(w, "# HELP rt_agent_memory_used_pct Host RAM used percent\n")
	fmt.Fprintf(w, "# TYPE rt_agent_memory_used_pct gauge\n")
	fmt.Fprintf(w, "rt_agent_memory_used_pct %f\n", rep.Memory.UsedPct)
	fmt.Fprintf(w, "# HELP rt_agent_swap_used_pct Host swap used percent\n")
	fmt.Fprintf(w, "# TYPE rt_agent_swap_used_pct gauge\n")
	fmt.Fprintf(w, "rt_agent_swap_used_pct %f\n", rep.Memory.SwapUsedPct)
	fmt.Fprintf(w, "# HELP rt_agent_degraded 1 if any hard degraded_reasons present\n")
	fmt.Fprintf(w, "# TYPE rt_agent_degraded gauge\n")
	fmt.Fprintf(w, "rt_agent_degraded %d\n", boolI(rep.Degraded))
	if rep.CPU.UsagePct != nil {
		fmt.Fprintf(w, "rt_agent_cpu_usage_pct %f\n", *rep.CPU.UsagePct)
	}

	// GPU surface.
	for _, g := range rep.GPUs {
		fmt.Fprintf(w, "rt_agent_gpu_vram_used_pct{index=\"%d\",name=%q} %f\n",
			g.Index, g.Name, g.VRAMUsedPct)
		fmt.Fprintf(w, "rt_agent_gpu_util_pct{index=\"%d\"} %d\n", g.Index, g.UtilPct)
		fmt.Fprintf(w, "rt_agent_gpu_temp_c{index=\"%d\"} %d\n", g.Index, g.TempC)
		fmt.Fprintf(w, "rt_agent_gpu_power_w{index=\"%d\"} %d\n", g.Index, g.PowerW)
		if g.ECCVolatileUncorrected != nil {
			fmt.Fprintf(w, "rt_agent_gpu_ecc_volatile_uncorrected_total{index=\"%d\"} %d\n",
				g.Index, *g.ECCVolatileUncorrected)
		}
		if g.NVLink != nil {
			for _, l := range g.NVLink.Links {
				up := 0
				if l.State == "Up" {
					up = 1
				}
				fmt.Fprintf(w, "rt_agent_gpu_nvlink_up{index=\"%d\",link=\"%d\"} %d\n",
					g.Index, l.Link, up)
				if l.SpeedGBPerS > 0 {
					fmt.Fprintf(w, "rt_agent_gpu_nvlink_speed_gbps{index=\"%d\",link=\"%d\"} %d\n",
						g.Index, l.Link, l.SpeedGBPerS)
				}
			}
		}
	}

	// Platform / model surface. Both backends emit under the same names so
	// dashboards stay simple.
	fmt.Fprintf(w, "rt_agent_ollama_up %d\n", boolI(rep.Ollama.Up))
	for name, p := range rep.Platforms {
		fmt.Fprintf(w, "rt_node_platform_up{platform=%q} %d\n", name, boolI(p.Up))
		for _, m := range p.Models {
			fmt.Fprintf(w, "rt_node_model_loaded{platform=%q,model=%q} %d\n", name, m.Name, boolI(m.Loaded))
			if m.SizeMB != nil {
				fmt.Fprintf(w, "rt_node_model_size_mb{platform=%q,model=%q} %d\n", name, m.Name, *m.SizeMB)
			}
			if m.VRAMUsedMB != nil {
				fmt.Fprintf(w, "rt_node_model_vram_used_mb{platform=%q,model=%q} %d\n", name, m.Name, *m.VRAMUsedMB)
			}
			if m.Queue != nil {
				if m.Queue.Running != nil {
					fmt.Fprintf(w, "rt_node_model_queue_running{platform=%q,model=%q} %d\n", name, m.Name, *m.Queue.Running)
				}
				if m.Queue.Waiting != nil {
					fmt.Fprintf(w, "rt_node_model_queue_waiting{platform=%q,model=%q} %d\n", name, m.Name, *m.Queue.Waiting)
				}
			}
			if m.KVCache != nil {
				if m.KVCache.GPUUsagePct != nil {
					fmt.Fprintf(w, "rt_node_model_kv_cache_gpu_usage_pct{platform=%q,model=%q} %f\n", name, m.Name, *m.KVCache.GPUUsagePct)
				}
				if m.KVCache.PrefixCacheHitRate != nil {
					fmt.Fprintf(w, "rt_node_model_kv_cache_prefix_hit_rate{platform=%q,model=%q} %f\n", name, m.Name, *m.KVCache.PrefixCacheHitRate)
				}
			}
			if m.Latency != nil {
				if m.Latency.TTFTp50 != nil {
					fmt.Fprintf(w, "rt_node_model_ttft_seconds{platform=%q,model=%q,quantile=\"0.5\"} %f\n", name, m.Name, *m.Latency.TTFTp50/1000.0)
				}
				if m.Latency.TTFTp99 != nil {
					fmt.Fprintf(w, "rt_node_model_ttft_seconds{platform=%q,model=%q,quantile=\"0.99\"} %f\n", name, m.Name, *m.Latency.TTFTp99/1000.0)
				}
				if m.Latency.TPOTp50 != nil {
					fmt.Fprintf(w, "rt_node_model_tpot_seconds{platform=%q,model=%q,quantile=\"0.5\"} %f\n", name, m.Name, *m.Latency.TPOTp50/1000.0)
				}
			}
			if m.Counters != nil {
				if m.Counters.RequestsSuccessTotal != nil {
					fmt.Fprintf(w, "rt_node_model_requests_success_total{platform=%q,model=%q} %d\n", name, m.Name, *m.Counters.RequestsSuccessTotal)
				}
				if m.Counters.PromptTokensTotal != nil {
					fmt.Fprintf(w, "rt_node_model_prompt_tokens_total{platform=%q,model=%q} %d\n", name, m.Name, *m.Counters.PromptTokensTotal)
				}
				if m.Counters.GenerationTokensTotal != nil {
					fmt.Fprintf(w, "rt_node_model_generation_tokens_total{platform=%q,model=%q} %d\n", name, m.Name, *m.Counters.GenerationTokensTotal)
				}
			}
		}
	}

	// Allocators (legacy gliner2-service + new training-process).
	for _, a := range rep.ServiceAllocs {
		fmt.Fprintf(w, "rt_agent_service_reserved_mb{name=%q} %f\n", a.Name, a.ReservedMB)
		fmt.Fprintf(w, "rt_agent_service_allocated_mb{name=%q} %f\n", a.Name, a.AllocatedMB)
	}

	// Disk.
	for _, d := range rep.Disk {
		fmt.Fprintf(w, "rt_node_disk_used_pct{path=%q} %f\n", d.Path, d.UsedPct)
		fmt.Fprintf(w, "rt_node_disk_total_gb{path=%q} %f\n", d.Path, d.TotalGB)
	}

	// Mode and training info (one of each is set; the rest are 0 so
	// alerts can `rt_node_mode{mode="training_mode"} == 1` cleanly).
	for _, m := range []string{"idle", "inference", "training_mode"} {
		v := 0
		if rep.Mode == m {
			v = 1
		}
		fmt.Fprintf(w, "rt_node_mode{mode=%q} %d\n", m, v)
	}
	if rep.Training != nil {
		fmt.Fprintf(w, "rt_node_training_run_id_info{run_id=%q} 1\n", rep.Training.RunID)
		remaining := int64(0)
		if rep.Training.ExpectedDurationS > 0 {
			elapsed := rep.Ts - rep.Training.EnteredAt
			remaining = rep.Training.ExpectedDurationS - elapsed
			if remaining < 0 {
				remaining = 0
			}
		}
		fmt.Fprintf(w, "rt_node_training_seconds_remaining %d\n", remaining)
	}

	// RDMA (Linux DGX-class only — block omitted otherwise).
	if rep.RDMA != nil {
		for _, d := range rep.RDMA.Devices {
			active := 0
			if d.State == "ACTIVE" {
				active = 1
			}
			fmt.Fprintf(w, "rt_node_rdma_device_active{device=%q,port=\"%d\"} %d\n", d.Name, d.Port, active)
			fmt.Fprintf(w, "rt_node_rdma_link_rate_gbps{device=%q,port=\"%d\"} %d\n", d.Name, d.Port, d.RateGbps)
			fmt.Fprintf(w, "rt_node_rdma_xmit_bytes_total{device=%q,port=\"%d\"} %d\n", d.Name, d.Port, d.Counters.PortXmitDataBytes)
			fmt.Fprintf(w, "rt_node_rdma_rcv_bytes_total{device=%q,port=\"%d\"} %d\n", d.Name, d.Port, d.Counters.PortRcvDataBytes)
			fmt.Fprintf(w, "rt_node_rdma_symbol_errors_total{device=%q,port=\"%d\"} %d\n", d.Name, d.Port, d.Counters.SymbolErrorCounter)
			fmt.Fprintf(w, "rt_node_rdma_link_recovery_total{device=%q,port=\"%d\"} %d\n", d.Name, d.Port, d.Counters.LinkErrorRecovery)
			fmt.Fprintf(w, "rt_node_rdma_link_downed_total{device=%q,port=\"%d\"} %d\n", d.Name, d.Port, d.Counters.LinkDowned)
		}
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
