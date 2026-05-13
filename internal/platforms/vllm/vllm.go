// Package vllm probes a local vLLM server and surfaces it as a
// platforms.Report. Two endpoints are consulted:
//
//   GET /v1/models     — OpenAI-compatible model list (the "is up" probe)
//   GET /metrics       — Prometheus text format with vllm:* series for per-model
//                        queue depth, KV cache, latency histograms, throughput
//
// Both are cached on a 5s TTL so a hot /health loop doesn't hammer vLLM.
// Latency histogram → percentile uses linear interpolation between bucket
// edges (same approach as Prometheus's histogram_quantile).
package vllm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/redtorchinc/node-agent/internal/config"
	"github.com/redtorchinc/node-agent/internal/platforms"

	"github.com/shirou/gopsutil/v3/process"
)

// Detector scrapes vLLM. Safe for concurrent Probe() calls.
type Detector struct {
	cfg config.PlatformEntry

	http *http.Client
	now  func() time.Time

	mu        sync.Mutex
	cached    *platforms.Report
	cachedAt  time.Time
	cacheTTL  time.Duration
	lastProm  *promSnapshot // for rate computation (one previous scrape)
}

// New returns a detector for the given platform entry.
func New(entry config.PlatformEntry) *Detector {
	return &Detector{
		cfg:  entry,
		http: &http.Client{Timeout: 2 * time.Second},
		now:  time.Now,
		// 30s matches the case-manager's response cache so backend polls
		// always hit warm. The keep-warm ticker in
		// internal/health/StartBackground refreshes under this TTL so
		// idle agents stay warm.
		cacheTTL: 30 * time.Second,
	}
}

// Name returns "vllm".
func (d *Detector) Name() string { return "vllm" }

// Refresh clears the cached report and runs Probe immediately. Used by
// the keep-warm ticker in internal/health/StartBackground so /health
// readers always hit a populated cache, not a cold cache primed by
// their own request.
func (d *Detector) Refresh(ctx context.Context) {
	d.mu.Lock()
	d.cached = nil
	d.cachedAt = time.Time{}
	d.mu.Unlock()
	_ = d.Probe(ctx)
}

// Probe assembles a platforms.Report. Returns Up=false on any unreachable
// endpoint. When `enabled: false` short-circuits without a network call.
func (d *Detector) Probe(ctx context.Context) platforms.Report {
	intervalS := int64(d.cacheTTL / time.Second)
	if d.cfg.Enabled == "false" {
		return platforms.Report{
			Up:             false,
			Endpoint:       d.cfg.Endpoint,
			Models:         []platforms.Model{},
			ProbeIntervalS: intervalS,
		}
	}
	d.mu.Lock()
	if d.cached != nil && d.now().Sub(d.cachedAt) < d.cacheTTL {
		cached := *d.cached
		d.mu.Unlock()
		return cached
	}
	d.mu.Unlock()

	rep := platforms.Report{
		Endpoint:       d.cfg.Endpoint,
		Models:         []platforms.Model{},
		Runners:        []platforms.Runner{},
		LastScrapeTS:   d.now().Unix(),
		ProbeIntervalS: intervalS,
	}

	models, version, err := d.fetchModels(ctx)
	if err != nil {
		rep.LastError = trimErr(err.Error())
		// Still try /metrics — vLLM sometimes serves /metrics while /v1/models
		// is starting up. But if /v1/models truly fails, treat as down.
	} else {
		rep.Up = true
		rep.Version = version
	}

	var prom *promSnapshot
	if metrics, perr := d.fetchMetrics(ctx); perr == nil {
		prom = parsePrometheus(metrics)
	}
	if rep.Up && prom == nil {
		// /v1/models works but /metrics doesn't — surface models without
		// metric-derived fields. Better than no entry at all.
	}

	// Build per-model entries from /v1/models, layered with /metrics where
	// available.
	for _, m := range models {
		md := buildModel(m, prom, rep.LastScrapeTS, d.lastProm, time.Duration(d.cacheTTL))
		rep.Models = append(rep.Models, md)
	}
	rep.Runners = probeVLLMRunners()

	// Cache for next call.
	d.mu.Lock()
	cp := rep
	d.cached = &cp
	d.cachedAt = d.now()
	if prom != nil {
		d.lastProm = prom
	}
	d.mu.Unlock()
	return rep
}

// fetchModels queries /v1/models. The OpenAI-compatible shape is
//
//	{ "data": [{ "id": "...", "created": ..., ... }], "object": "list" }
type modelsResp struct {
	Data []struct {
		ID       string `json:"id"`
		Created  int64  `json:"created"`
		OwnedBy  string `json:"owned_by"`
		// vLLM-specific extras (best-effort; field names vary across versions).
		MaxModelLen int    `json:"max_model_len"`
		Quant       string `json:"quantization"`
	} `json:"data"`
}

func (d *Detector) fetchModels(ctx context.Context) ([]vllmModel, string, error) {
	url := strings.TrimRight(d.cfg.Endpoint, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("/v1/models: http %d", resp.StatusCode)
	}
	var p modelsResp
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, "", err
	}
	version := resp.Header.Get("X-VLLM-Version") // best-effort
	out := make([]vllmModel, 0, len(p.Data))
	for _, m := range p.Data {
		out = append(out, vllmModel{
			ID:           m.ID,
			MaxModelLen:  m.MaxModelLen,
			Quantization: m.Quant,
		})
	}
	return out, version, nil
}

type vllmModel struct {
	ID           string
	MaxModelLen  int
	Quantization string
}

func (d *Detector) fetchMetrics(ctx context.Context) (string, error) {
	url := d.cfg.MetricsEndpoint
	if url == "" {
		url = strings.TrimRight(d.cfg.Endpoint, "/") + "/metrics"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("/metrics: http %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func buildModel(m vllmModel, prom *promSnapshot, ts int64, prev *promSnapshot, interval time.Duration) platforms.Model {
	out := platforms.Model{
		Name:         m.ID,
		Platform:     "vllm",
		Loaded:       true,
		Quantization: m.Quantization,
		LastScrapeTS: ts,
	}
	if m.MaxModelLen > 0 {
		out.ContextWindow = platforms.IntPtr(m.MaxModelLen)
		out.MaxModelLen = platforms.IntPtr(m.MaxModelLen)
	}
	// vLLM is GPU-only by design — no CPU offload path.
	out.ProcessorSplit = "100% GPU"

	if prom == nil {
		return out
	}

	label := map[string]string{"model_name": m.ID}
	if v, ok := prom.gauge("vllm:num_requests_running", label); ok {
		q := &platforms.Queue{}
		q.Running = platforms.IntPtr(int(v))
		if w, ok := prom.gauge("vllm:num_requests_waiting", label); ok {
			q.Waiting = platforms.IntPtr(int(w))
		}
		if s, ok := prom.gauge("vllm:num_requests_swapped", label); ok {
			q.Swapped = platforms.IntPtr(int(s))
		}
		out.Queue = q
	}
	kv := &platforms.KVCache{}
	if v, ok := prom.gauge("vllm:gpu_cache_usage_perc", label); ok {
		kv.GPUUsagePct = platforms.Float64Ptr(v * 100)
	}
	if v, ok := prom.gauge("vllm:cpu_cache_usage_perc", label); ok {
		kv.CPUUsagePct = platforms.Float64Ptr(v * 100)
	}
	if v, ok := prom.gauge("vllm:gpu_prefix_cache_hit_rate", label); ok {
		kv.PrefixCacheHitRate = platforms.Float64Ptr(v)
	}
	if kv.GPUUsagePct != nil || kv.CPUUsagePct != nil || kv.PrefixCacheHitRate != nil {
		out.KVCache = kv
	}

	lat := &platforms.Latency{}
	if p50, ok := prom.histogramQuantile("vllm:time_to_first_token_seconds", label, 0.5); ok {
		lat.TTFTp50 = platforms.Float64Ptr(p50 * 1000)
	}
	if p99, ok := prom.histogramQuantile("vllm:time_to_first_token_seconds", label, 0.99); ok {
		lat.TTFTp99 = platforms.Float64Ptr(p99 * 1000)
	}
	if p50, ok := prom.histogramQuantile("vllm:time_per_output_token_seconds", label, 0.5); ok {
		lat.TPOTp50 = platforms.Float64Ptr(p50 * 1000)
	}
	if p99, ok := prom.histogramQuantile("vllm:time_per_output_token_seconds", label, 0.99); ok {
		lat.TPOTp99 = platforms.Float64Ptr(p99 * 1000)
	}
	if p50, ok := prom.histogramQuantile("vllm:e2e_request_latency_seconds", label, 0.5); ok {
		lat.E2Ep50 = platforms.Float64Ptr(p50 * 1000)
	}
	if p99, ok := prom.histogramQuantile("vllm:e2e_request_latency_seconds", label, 0.99); ok {
		lat.E2Ep99 = platforms.Float64Ptr(p99 * 1000)
	}
	if lat.TTFTp50 != nil || lat.TTFTp99 != nil || lat.TPOTp50 != nil || lat.E2Ep50 != nil {
		out.Latency = lat
	}

	counters := &platforms.Counters{}
	if v, ok := prom.counter("vllm:request_success_total", label); ok {
		counters.RequestsSuccessTotal = platforms.Uint64Ptr(uint64(v))
	}
	if v, ok := prom.counter("vllm:prompt_tokens_total", label); ok {
		counters.PromptTokensTotal = platforms.Uint64Ptr(uint64(v))
	}
	if v, ok := prom.counter("vllm:generation_tokens_total", label); ok {
		counters.GenerationTokensTotal = platforms.Uint64Ptr(uint64(v))
	}
	if counters.RequestsSuccessTotal != nil || counters.PromptTokensTotal != nil || counters.GenerationTokensTotal != nil {
		out.Counters = counters
	}

	// Throughput: derive token-rates from two consecutive scrapes.
	if prev != nil && interval > 0 {
		thr := &platforms.Throughput{}
		secs := interval.Seconds()
		if secs <= 0 {
			secs = 1
		}
		if cur, ok := prom.counter("vllm:prompt_tokens_total", label); ok {
			if old, ok2 := prev.counter("vllm:prompt_tokens_total", label); ok2 && cur >= old {
				thr.PromptTokensPerS = platforms.Float64Ptr((cur - old) / secs)
			}
		}
		if cur, ok := prom.counter("vllm:generation_tokens_total", label); ok {
			if old, ok2 := prev.counter("vllm:generation_tokens_total", label); ok2 && cur >= old {
				thr.GenerationTokensPerS = platforms.Float64Ptr((cur - old) / secs)
			}
		}
		if thr.PromptTokensPerS != nil && thr.GenerationTokensPerS != nil {
			thr.TokensPerS = platforms.Float64Ptr(*thr.PromptTokensPerS + *thr.GenerationTokensPerS)
		}
		if thr.PromptTokensPerS != nil || thr.GenerationTokensPerS != nil {
			out.Throughput = thr
		}
	}

	return out
}

// probeVLLMRunners enumerates processes that look like vLLM workers. Match
// is best-effort: cmdline starts with "vllm" or contains "vllm.entrypoints".
func probeVLLMRunners() []platforms.Runner {
	procs, err := process.Processes()
	if err != nil {
		return []platforms.Runner{}
	}
	out := []platforms.Runner{}
	for _, p := range procs {
		cmd, _ := p.Cmdline()
		lc := strings.ToLower(cmd)
		if !strings.Contains(lc, "vllm") {
			continue
		}
		if !strings.Contains(lc, "vllm.entrypoints") && !strings.HasPrefix(lc, "vllm") {
			continue
		}
		cpu, _ := p.CPUPercent()
		rss := int64(0)
		if mi, err := p.MemoryInfo(); err == nil && mi != nil {
			rss = int64(mi.RSS / 1024 / 1024)
		}
		out = append(out, platforms.Runner{
			PID:    int(p.Pid),
			CPUPct: round2(cpu),
			RSSMB:  rss,
		})
	}
	return out
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

func trimErr(s string) string {
	const max = 200
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
