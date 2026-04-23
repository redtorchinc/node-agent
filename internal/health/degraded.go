package health

import (
	"time"

	"github.com/redtorchinc/node-agent/internal/config"
)

// Reason strings — do NOT rename. They are read directly by the case-manager's
// rank_nodes() (see SPEC.md §degraded_reasons). Add new ones by appending;
// never remove.
const (
	ReasonOllamaDown                = "ollama_down"
	ReasonSwapOver75pct             = "swap_over_75pct"
	ReasonVRAMOver95pct             = "vram_over_95pct"
	ReasonAgentStale                = "agent_stale"
	ReasonVRAMServiceCreepCritical  = "vram_service_creep_critical"

	ReasonSwapOver50pct             = "swap_over_50pct"
	ReasonVRAMOver90pct             = "vram_over_90pct"
	ReasonLoadAvgOver2xCores        = "load_avg_over_2x_cores"
	ReasonOllamaRunnerStuck         = "ollama_runner_stuck"
	ReasonVRAMServiceCreepWarn      = "vram_service_creep_warn"
)

// hardReasons is the set whose presence sets Report.Degraded=true. All
// other reasons are "soft" (deprioritize but usable).
var hardReasons = map[string]struct{}{
	ReasonOllamaDown:               {},
	ReasonSwapOver75pct:            {},
	ReasonVRAMOver95pct:            {},
	ReasonAgentStale:               {},
	ReasonVRAMServiceCreepCritical: {},
}

// Evaluate is the pure function at the heart of degraded-state detection.
// Inputs: a fully-populated Report, the active config (for runtime-tunable
// thresholds), and the current time. Output: (anyHard, reasons in SPEC order).
//
// All thresholds in this function default to the values stated in SPEC.md
// §degraded_reasons. Config overrides are optional and tracked per-reason.
func Evaluate(r Report, cfg config.Config, now time.Time) (bool, []string) {
	var reasons []string

	// --- hard reasons, in SPEC order ---

	if !r.Ollama.Up {
		reasons = append(reasons, ReasonOllamaDown)
	}
	if r.Memory.SwapUsedPct > 75 {
		reasons = append(reasons, ReasonSwapOver75pct)
	}
	if maxVRAMPct(r) > 95 {
		reasons = append(reasons, ReasonVRAMOver95pct)
	}
	if r.Ollama.LastProbe > 0 && now.Unix()-r.Ollama.LastProbe > 60 {
		reasons = append(reasons, ReasonAgentStale)
	}
	if serviceCreep(r, creepCritical) {
		reasons = append(reasons, ReasonVRAMServiceCreepCritical)
	}

	// --- soft reasons, in SPEC order ---

	if r.Memory.SwapUsedPct > 50 && r.Memory.SwapUsedPct <= 75 {
		reasons = append(reasons, ReasonSwapOver50pct)
	}
	if p := maxVRAMPct(r); p > 90 && p <= 95 {
		reasons = append(reasons, ReasonVRAMOver90pct)
	}
	if r.CPU.CoresLogical > 0 && r.CPU.Load1m > float64(2*r.CPU.CoresLogical) {
		reasons = append(reasons, ReasonLoadAvgOver2xCores)
	}
	if runnerStuck(r) {
		reasons = append(reasons, ReasonOllamaRunnerStuck)
	}
	if serviceCreep(r, creepWarn) {
		reasons = append(reasons, ReasonVRAMServiceCreepWarn)
	}

	_ = cfg // reserved for future threshold overrides; keep signature stable

	degraded := false
	for _, r := range reasons {
		if _, ok := hardReasons[r]; ok {
			degraded = true
			break
		}
	}
	return degraded, reasons
}

// maxVRAMPct returns the largest VRAMUsedPct across all GPUs, or 0 if none.
// On Apple Silicon (unified memory) the GPU entries have VRAMTotalMB=0 so
// this returns 0 — use mem.UsedPct instead via the caller. That's by design:
// the ranker reads memory.unified and mem.used_pct on those boxes.
func maxVRAMPct(r Report) float64 {
	var m float64
	for _, g := range r.GPUs {
		if g.VRAMUsedPct > m {
			m = g.VRAMUsedPct
		}
	}
	return m
}

type creepLevel int

const (
	creepWarn creepLevel = iota
	creepCritical
)

// serviceCreep returns true if any tracked allocator exceeds the given
// severity level. See SPEC.md §degraded_reasons for the ratio/threshold math.
//
//	critical: reserved/allocated > 3.0 AND reserved > ThresholdCritMB
//	warn:     reserved/allocated > 2.0 (severity-capped by critical)
func serviceCreep(r Report, level creepLevel) bool {
	for _, s := range r.ServiceAllocs {
		if !s.ScrapeOK || s.AllocatedMB <= 0 {
			continue
		}
		ratio := s.ReservedMB / s.AllocatedMB
		switch level {
		case creepCritical:
			if ratio > 3.0 && s.ReservedMB > float64(s.ThresholdCritMB) {
				return true
			}
		case creepWarn:
			// "warn" only fires if we're not already in critical territory
			// for the same entry — the backend consumes both reasons but
			// emitting warn alongside critical adds noise.
			critical := ratio > 3.0 && s.ReservedMB > float64(s.ThresholdCritMB)
			if !critical && ratio > 2.0 && s.ReservedMB > float64(s.ThresholdWarnMB) {
				return true
			}
		}
	}
	return false
}

// runnerStuck implements `ollama_runner_stuck`: runner PID exists, CPU 0%,
// and we have pending work. We can't see "queued requests" from here, so
// v1 only flags when CPUPct is near zero while at least one model is loaded.
// Refine once we have a queue-depth signal.
func runnerStuck(r Report) bool {
	if len(r.Ollama.Models) == 0 {
		return false
	}
	for _, rn := range r.Ollama.Runners {
		if rn.CPUPct < 0.5 {
			return true
		}
	}
	return false
}
