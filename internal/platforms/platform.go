// Package platforms unifies the per-platform inference surface (Ollama,
// vLLM, ...) under one schema. Each /health response carries a `platforms`
// map keyed by platform name; each entry has the same shape regardless of
// which platform served it. Fields a platform genuinely can't supply are
// left nil (and omitted via `omitempty`) — silence beats fabrication.
//
// Per-platform detectors live in subpackages (platforms/ollama,
// platforms/vllm). They each return a canonical platforms.Report that the
// health reporter aggregates.
package platforms

import "context"

// Platform is the contract every detector implements. Probe must be safe
// for concurrent callers and respect ctx — /health applies its own outer
// timeout and a hung detector should not stall the whole report.
type Platform interface {
	Name() string
	Probe(ctx context.Context) Report
}

// Report is the per-platform entry under /health.platforms.{name}.
type Report struct {
	// Up is true if the platform is reachable and reporting.
	Up bool `json:"up"`

	// Endpoint is the base URL the agent probed. Always set, even when Up=false,
	// so operators can see where the agent looked.
	Endpoint string `json:"endpoint,omitempty"`

	// Version is the platform's self-reported version, when available.
	Version string `json:"version,omitempty"`

	// Models lists every resident/loaded model on this platform.
	Models []Model `json:"models"`

	// Runners is per-process accounting of the platform's worker processes.
	Runners []Runner `json:"runners,omitempty"`

	// LastScrapeTS is when the metrics/state were last refreshed.
	LastScrapeTS int64 `json:"last_scrape_ts,omitempty"`

	// ProbeIntervalS is the detector's internal cache TTL in seconds — the
	// expected refresh cadence for this entry. Self-describing so the
	// backend's staleness check doesn't have to hardcode a threshold.
	ProbeIntervalS int64 `json:"probe_interval_s,omitempty"`

	// Stale is true when LastScrapeTS is older than 3 × ProbeIntervalS.
	// Agent-side flag — the backend can use it without re-implementing the
	// math. Informational; doesn't fire a degraded_reason on its own (the
	// existing top-level agent_stale handles the Ollama-specific case).
	Stale bool `json:"stale,omitempty"`

	// LastError surfaces the most recent probe error string, if any. Empty on
	// success. Trimmed to avoid leaking internal hostnames in nested error
	// chains.
	LastError string `json:"last_error,omitempty"`
}

// Runner is one worker process (e.g. Ollama runner, vLLM worker).
type Runner struct {
	PID    int     `json:"pid"`
	CPUPct float64 `json:"cpu_pct"`
	RSSMB  int64   `json:"rss_mb"`

	// QueueDepth is set when the platform exposes per-process queue stats.
	// vLLM reports this via /metrics; Ollama doesn't.
	QueueDepth *int `json:"queue_depth,omitempty"`
}

// Model is the canonical per-model schema. See V0_2_0_PLAN.md §A2.1 for the
// source-of-truth table per platform. Pointer fields express "platform did
// not provide" via JSON omission — they are NOT zero-valued silently.
type Model struct {
	Name           string `json:"name"`
	Platform       string `json:"platform"`
	Loaded         bool   `json:"loaded"`
	SizeMB         *int64 `json:"size_mb,omitempty"`
	Quantization   string `json:"quantization,omitempty"`
	ContextWindow  *int   `json:"context_window,omitempty"`
	MaxModelLen    *int   `json:"max_model_len,omitempty"`
	ProcessorSplit string `json:"processor_split,omitempty"`
	TTLs           *int64 `json:"ttl_s,omitempty"`
	VRAMUsedMB     *int64 `json:"vram_used_mb,omitempty"`

	Queue      *Queue      `json:"queue,omitempty"`
	KVCache    *KVCache    `json:"kv_cache,omitempty"`
	Latency    *Latency    `json:"latency_ms,omitempty"`
	Throughput *Throughput `json:"throughput,omitempty"`
	Counters   *Counters   `json:"counters,omitempty"`

	LastScrapeTS int64 `json:"last_scrape_ts,omitempty"`
}

// Queue carries request-queue depth, primarily vLLM-sourced.
type Queue struct {
	Running *int `json:"running,omitempty"`
	Waiting *int `json:"waiting,omitempty"`
	Swapped *int `json:"swapped,omitempty"`
}

// KVCache reports vLLM's KV cache utilization and prefix-cache hit rate.
type KVCache struct {
	GPUUsagePct        *float64 `json:"gpu_usage_pct,omitempty"`
	CPUUsagePct        *float64 `json:"cpu_usage_pct,omitempty"`
	PrefixCacheHitRate *float64 `json:"prefix_cache_hit_rate,omitempty"`
}

// Latency reports request-latency percentiles in milliseconds.
type Latency struct {
	TTFTp50 *float64 `json:"ttft_p50,omitempty"`
	TTFTp99 *float64 `json:"ttft_p99,omitempty"`
	TPOTp50 *float64 `json:"tpot_p50,omitempty"`
	TPOTp99 *float64 `json:"tpot_p99,omitempty"`
	E2Ep50  *float64 `json:"e2e_p50,omitempty"`
	E2Ep99  *float64 `json:"e2e_p99,omitempty"`
}

// Throughput reports tokens-per-second rates.
type Throughput struct {
	PromptTokensPerS     *float64 `json:"prompt_tokens_per_s,omitempty"`
	GenerationTokensPerS *float64 `json:"generation_tokens_per_s,omitempty"`
	TokensPerS           *float64 `json:"tokens_per_s,omitempty"`
}

// Counters are cumulative since the platform started. Use rate() over them.
type Counters struct {
	RequestsSuccessTotal  *uint64 `json:"requests_success_total,omitempty"`
	RequestsFailedTotal   *uint64 `json:"requests_failed_total,omitempty"`
	PromptTokensTotal     *uint64 `json:"prompt_tokens_total,omitempty"`
	GenerationTokensTotal *uint64 `json:"generation_tokens_total,omitempty"`
}

// Helper constructors so call sites can write IntPtr(n) instead of taking
// the address of a temporary.

// IntPtr returns a *int holding v.
func IntPtr(v int) *int { return &v }

// Int64Ptr returns a *int64 holding v.
func Int64Ptr(v int64) *int64 { return &v }

// Uint64Ptr returns a *uint64 holding v.
func Uint64Ptr(v uint64) *uint64 { return &v }

// Float64Ptr returns a *float64 holding v.
func Float64Ptr(v float64) *float64 { return &v }
