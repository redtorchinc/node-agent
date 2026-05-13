package health

import (
	"reflect"
	"testing"
	"time"

	"github.com/redtorchinc/node-agent/internal/allocators"
	"github.com/redtorchinc/node-agent/internal/config"
	"github.com/redtorchinc/node-agent/internal/gpu"
	"github.com/redtorchinc/node-agent/internal/mem"
	"github.com/redtorchinc/node-agent/internal/ollama"
	"github.com/redtorchinc/node-agent/internal/platforms"
)

// cleanReport returns a Report with nothing wrong: ollama up, low memory,
// one GPU at 50%, recent probe, no creep. Each test mutates this to isolate
// one reason at a time.
func cleanReport(now time.Time) Report {
	return Report{
		CPU:    CPUInfo{CoresLogical: 16, Load1m: 4},
		Memory: mem.Info{SwapUsedPct: 10},
		GPUs: []gpu.GPU{{
			VRAMTotalMB: 24576, VRAMUsedMB: 12288, VRAMUsedPct: 50,
		}},
		Ollama: ollama.Info{
			Up:        true,
			LastProbe: now.Unix(),
			Models:    []ollama.Model{{Name: "m", SizeMB: 100}},
			Runners:   []ollama.Runner{{PID: 1, CPUPct: 100}},
		},
	}
}

func TestEvaluate_Clean(t *testing.T) {
	now := time.Unix(1713820000, 0)
	deg, reasons := Evaluate(cleanReport(now), config.Config{}, now)
	if deg || len(reasons) != 0 {
		t.Errorf("clean should not be degraded: %v %v", deg, reasons)
	}
}

func TestEvaluate_OllamaDown(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.Ollama.Up = false
	deg, reasons := Evaluate(r, config.Config{}, now)
	if !deg || !contains(reasons, ReasonOllamaDown) {
		t.Errorf("want ollama_down hard, got %v %v", deg, reasons)
	}
}

// Issue #11: vllm_down must NOT fire on hosts that have not explicitly
// opted into vLLM monitoring. Before v0.2.8, "auto" (the default) fired
// vllm_down whenever the probe failed — flooding every Ollama-only host
// with the noise. Now only enabled=true fires.
func TestEvaluate_VLLMAutoDoesNotFireVLLMDown(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	// Simulate a vLLM probe that failed (typical Ollama-only host).
	r.Platforms = map[string]platforms.Report{
		"vllm": {Up: false, Endpoint: "http://localhost:8000"},
	}
	cfg := config.Config{}
	cfg.Platforms.VLLM.Enabled = "auto"
	_, reasons := Evaluate(r, cfg, now)
	if contains(reasons, ReasonVLLMDown) {
		t.Errorf("vllm_down must NOT fire on `enabled: auto` host: %v", reasons)
	}
}

// With explicit `enabled: true`, vllm_down fires on probe failure
// (operator opted into being told about vLLM outages).
func TestEvaluate_VLLMTrueFiresVLLMDown(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.Platforms = map[string]platforms.Report{
		"vllm": {Up: false, Endpoint: "http://localhost:8000"},
	}
	cfg := config.Config{}
	cfg.Platforms.VLLM.Enabled = "true"
	_, reasons := Evaluate(r, cfg, now)
	if !contains(reasons, ReasonVLLMDown) {
		t.Errorf("vllm_down must fire on `enabled: true` host with down probe: %v", reasons)
	}
}

// Issue #12: hasSoftReason must distinguish hard vs. soft reasons so the
// composer can set degraded_soft correctly. Mixed case: both kinds fire.
func TestHasSoftReason(t *testing.T) {
	if hasSoftReason(nil) {
		t.Errorf("empty reasons → no soft")
	}
	if hasSoftReason([]string{ReasonOllamaDown}) {
		t.Errorf("only hard reasons → hasSoftReason=false")
	}
	if !hasSoftReason([]string{ReasonSwapOver50pct}) {
		t.Errorf("soft reason → hasSoftReason=true")
	}
	if !hasSoftReason([]string{ReasonOllamaDown, ReasonSwapOver50pct}) {
		t.Errorf("mixed hard+soft → hasSoftReason=true")
	}
}

// vLLM-only nodes set platforms.ollama.enabled=false. Ollama.Up keeps being
// reported truthfully as false, but ollama_down (and friends) must NOT fire
// in degraded_reasons — otherwise the ranker hard-skips a healthy vLLM box.
func TestEvaluate_OllamaDisabled_SuppressesOllamaReasons(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.Ollama.Up = false
	r.Ollama.LastProbe = now.Unix() - 120 // would otherwise trip agent_stale
	cfg := config.Config{}
	cfg.Platforms.Ollama.Enabled = "false"
	deg, reasons := Evaluate(r, cfg, now)
	if deg {
		t.Errorf("ollama-disabled node should not be degraded: %v", reasons)
	}
	if contains(reasons, ReasonOllamaDown) {
		t.Errorf("ollama_down must be suppressed when ollama.enabled=false: %v", reasons)
	}
	if contains(reasons, ReasonAgentStale) {
		t.Errorf("agent_stale must be suppressed when ollama.enabled=false: %v", reasons)
	}
}

func TestEvaluate_SwapOver75(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.Memory.SwapUsedPct = 80
	deg, reasons := Evaluate(r, config.Config{}, now)
	if !deg || !contains(reasons, ReasonSwapOver75pct) {
		t.Errorf("want swap_over_75pct hard: %v %v", deg, reasons)
	}
	// And not the soft variant.
	if contains(reasons, ReasonSwapOver50pct) {
		t.Errorf("should not emit soft swap_over_50pct when hard fires")
	}
}

func TestEvaluate_SwapOver50Soft(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.Memory.SwapUsedPct = 60
	deg, reasons := Evaluate(r, config.Config{}, now)
	if deg {
		t.Errorf("soft swap should not flip degraded")
	}
	if !contains(reasons, ReasonSwapOver50pct) {
		t.Errorf("want swap_over_50pct soft: %v", reasons)
	}
}

func TestEvaluate_VRAMOver95(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.GPUs[0].VRAMUsedPct = 97
	deg, reasons := Evaluate(r, config.Config{}, now)
	if !deg || !contains(reasons, ReasonVRAMOver95pct) {
		t.Errorf("want vram_over_95pct: %v %v", deg, reasons)
	}
}

func TestEvaluate_VRAMOver90Soft(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.GPUs[0].VRAMUsedPct = 92
	deg, reasons := Evaluate(r, config.Config{}, now)
	if deg {
		t.Errorf("soft vram should not flip degraded")
	}
	if !contains(reasons, ReasonVRAMOver90pct) {
		t.Errorf("want vram_over_90pct soft: %v", reasons)
	}
}

func TestEvaluate_AgentStale(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.Ollama.LastProbe = now.Unix() - 120
	deg, reasons := Evaluate(r, config.Config{}, now)
	if !deg || !contains(reasons, ReasonAgentStale) {
		t.Errorf("want agent_stale: %v %v", deg, reasons)
	}
}

func TestEvaluate_LoadAvgOver2xCores(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.CPU.Load1m = 33 // > 2*16
	_, reasons := Evaluate(r, config.Config{}, now)
	if !contains(reasons, ReasonLoadAvgOver2xCores) {
		t.Errorf("want load_avg_over_2x_cores: %v", reasons)
	}
}

// Regression for issue #1: a warm idle runner (CPU=0, model loaded, no
// queued requests) must NOT fire ollama_runner_stuck. Previously it
// did, false-positiving on every embed-serving node.
func TestEvaluate_OllamaRunnerStuck_WarmIdleDoesNotFire(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.Ollama.Runners[0].CPUPct = 0
	// cleanReport's single model has QueuedRequests default 0.
	_, reasons := Evaluate(r, config.Config{}, now)
	if contains(reasons, ReasonOllamaRunnerStuck) {
		t.Errorf("warm idle must not fire ollama_runner_stuck: %v", reasons)
	}
}

func TestEvaluate_OllamaRunnerStuck_FiresWithRealQueue(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.Ollama.Runners[0].CPUPct = 0
	r.Ollama.Models[0].QueuedRequests = 3
	_, reasons := Evaluate(r, config.Config{}, now)
	if !contains(reasons, ReasonOllamaRunnerStuck) {
		t.Errorf("want ollama_runner_stuck when cpu=0 AND queued_requests>0: %v", reasons)
	}
}

// An Ollama version that doesn't expose queued_requests at all (all zero)
// must never flip this soft reason, even with CPU=0 and models loaded.
func TestEvaluate_OllamaRunnerStuck_OldOllamaNoQueueField(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.Ollama.Runners[0].CPUPct = 0
	// simulate multiple models, all without queue visibility
	r.Ollama.Models = []ollama.Model{
		{Name: "a", QueuedRequests: 0},
		{Name: "b", QueuedRequests: 0},
	}
	_, reasons := Evaluate(r, config.Config{}, now)
	if contains(reasons, ReasonOllamaRunnerStuck) {
		t.Errorf("missing queue field must not fire: %v", reasons)
	}
}

func TestEvaluate_ServiceCreepCritical(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.ServiceAllocs = []allocators.Scraped{{
		Name:            "gliner2-service",
		ScrapeOK:        true,
		AllocatedMB:     2048,
		ReservedMB:      16384, // ratio = 8.0 — the 2026-04-22 incident
		ThresholdCritMB: 10240,
		ThresholdWarnMB: 4096,
	}}
	deg, reasons := Evaluate(r, config.Config{}, now)
	if !deg || !contains(reasons, ReasonVRAMServiceCreepCritical) {
		t.Errorf("want vram_service_creep_critical: %v %v", deg, reasons)
	}
	if contains(reasons, ReasonVRAMServiceCreepWarn) {
		t.Errorf("should not emit warn alongside critical")
	}
}

func TestEvaluate_ServiceCreepWarn(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.ServiceAllocs = []allocators.Scraped{{
		Name:            "gliner2-service",
		ScrapeOK:        true,
		AllocatedMB:     1000,
		ReservedMB:      5000, // ratio 5.0, but below critical threshold
		ThresholdCritMB: 10240,
		ThresholdWarnMB: 4096,
	}}
	deg, reasons := Evaluate(r, config.Config{}, now)
	if deg {
		t.Errorf("warn alone should not flip degraded")
	}
	if !contains(reasons, ReasonVRAMServiceCreepWarn) {
		t.Errorf("want warn: %v", reasons)
	}
}

func TestEvaluate_ServiceCreepIgnoresFailedScrape(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.ServiceAllocs = []allocators.Scraped{{
		Name: "x", ScrapeOK: false,
		AllocatedMB: 1, ReservedMB: 999, // ratio looks terrible but we ignore
		ThresholdCritMB: 10, ThresholdWarnMB: 5,
	}}
	deg, reasons := Evaluate(r, config.Config{}, now)
	if deg || contains(reasons, ReasonVRAMServiceCreepCritical) {
		t.Errorf("failed scrape must not propagate: %v %v", deg, reasons)
	}
}

func TestEvaluate_MultipleReasons(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.Memory.SwapUsedPct = 80
	r.GPUs[0].VRAMUsedPct = 97
	r.Ollama.Up = false
	deg, reasons := Evaluate(r, config.Config{}, now)
	if !deg {
		t.Errorf("want degraded")
	}
	// SPEC order: ollama_down → swap_over_75pct → vram_over_95pct
	want := []string{ReasonOllamaDown, ReasonSwapOver75pct, ReasonVRAMOver95pct}
	if !reflect.DeepEqual(reasons, want) {
		t.Errorf("order wrong: got %v want %v", reasons, want)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
