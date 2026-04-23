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
