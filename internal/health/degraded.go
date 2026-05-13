package health

import (
	"time"

	"github.com/redtorchinc/node-agent/internal/config"
)

// Reason strings — do NOT rename. They are read directly by the case-manager's
// rank_nodes() (see SPEC.md §degraded_reasons). Add new ones by appending;
// never remove.
const (
	// v0.1.x hard reasons
	ReasonOllamaDown               = "ollama_down"
	ReasonSwapOver75pct            = "swap_over_75pct"
	ReasonVRAMOver95pct            = "vram_over_95pct"
	ReasonAgentStale               = "agent_stale"
	ReasonVRAMServiceCreepCritical = "vram_service_creep_critical"
	// v0.1.x soft reasons
	ReasonSwapOver50pct        = "swap_over_50pct"
	ReasonVRAMOver90pct        = "vram_over_90pct"
	ReasonLoadAvgOver2xCores   = "load_avg_over_2x_cores"
	ReasonOllamaRunnerStuck    = "ollama_runner_stuck"
	ReasonVRAMServiceCreepWarn = "vram_service_creep_warn"

	// v0.2.0 hard reasons
	ReasonDiskOver98pct        = "disk_over_98pct"
	ReasonGPUECCUncorrected    = "gpu_ecc_uncorrected"
	ReasonVLLMRequiredDown     = "vllm_required_down"
	ReasonRDMAPortDown         = "rdma_port_down"
	ReasonRDMAPeermemMissing   = "rdma_peermem_missing"
	ReasonRDMACollectorStale   = "rdma_collector_stale"
	ReasonTrainingInProgress   = "training_in_progress"
	// v0.2.0 soft reasons
	ReasonDiskOver90pct        = "disk_over_90pct"
	ReasonClockSkewHigh        = "clock_skew_high"
	ReasonCPUThermalThrottling = "cpu_thermal_throttling"
	ReasonGPUThermalThrottling = "gpu_thermal_throttling"
	ReasonGPUPowerThrottling   = "gpu_power_throttling"
	ReasonVLLMDown             = "vllm_down"
	ReasonRDMAErrorsGrowing    = "rdma_errors_growing"
	ReasonRDMAPFCStorm         = "rdma_pfc_storm"
	ReasonRDMALinkDegraded     = "rdma_link_degraded"
)

// hasSoftReason reports whether any element of reasons is NOT in
// hardReasons. Used by the composer to set Report.DegradedSoft.
func hasSoftReason(reasons []string) bool {
	for _, r := range reasons {
		if _, hard := hardReasons[r]; !hard {
			return true
		}
	}
	return false
}

// hardReasons is the set whose presence sets Report.Degraded=true. All
// other reasons are "soft" (deprioritize but usable).
var hardReasons = map[string]struct{}{
	ReasonOllamaDown:               {},
	ReasonSwapOver75pct:            {},
	ReasonVRAMOver95pct:            {},
	ReasonAgentStale:               {},
	ReasonVRAMServiceCreepCritical: {},
	ReasonDiskOver98pct:            {},
	ReasonGPUECCUncorrected:        {},
	ReasonVLLMRequiredDown:         {},
	ReasonRDMAPortDown:             {},
	ReasonRDMAPeermemMissing:       {},
	ReasonRDMACollectorStale:       {},
	ReasonTrainingInProgress:       {},
}

// Evaluate is the pure function at the heart of degraded-state detection.
// Inputs: a fully-populated Report, the active config (for runtime-tunable
// thresholds), and the current time. Output: (anyHard, reasons in SPEC order).
//
// All thresholds in this function default to the values stated in SPEC.md
// §degraded_reasons. Config overrides are optional and tracked per-reason.
//
// Important rule: never fire a reason from a NIL metric. If a platform
// genuinely can't report (e.g. CPU temps unavailable without root on macOS),
// the corresponding reason is silent — silence beats a false "all clear".
func Evaluate(r Report, cfg config.Config, now time.Time) (bool, []string) {
	var reasons []string

	// Operator opt-out: when platforms.ollama.enabled is explicitly false, this
	// node doesn't run Ollama (e.g. vLLM-only GB10). r.Ollama.Up=false stays
	// truthful in the payload, but the Ollama-derived reasons (ollama_down,
	// agent_stale, ollama_runner_stuck) are suppressed so rank_nodes() doesn't
	// hard-skip the box.
	ollamaDisabled := cfg.Platforms.Ollama.Enabled == "false"

	// --- hard reasons, in SPEC order ---

	if !r.Ollama.Up && !ollamaDisabled {
		reasons = append(reasons, ReasonOllamaDown)
	}
	if r.Memory.SwapUsedPct > 75 {
		reasons = append(reasons, ReasonSwapOver75pct)
	}
	if maxVRAMPct(r) > 95 {
		reasons = append(reasons, ReasonVRAMOver95pct)
	}
	if !ollamaDisabled && r.Ollama.LastProbe > 0 && now.Unix()-r.Ollama.LastProbe > 60 {
		reasons = append(reasons, ReasonAgentStale)
	}
	if serviceCreep(r, creepCritical) {
		reasons = append(reasons, ReasonVRAMServiceCreepCritical)
	}
	if maxDiskPct(r) > 98 {
		reasons = append(reasons, ReasonDiskOver98pct)
	}
	if hasUncorrectableECC(r) {
		reasons = append(reasons, ReasonGPUECCUncorrected)
	}
	if vllmRequiredDown(r, cfg) {
		reasons = append(reasons, ReasonVLLMRequiredDown)
	}
	if r.RDMA != nil {
		if rdmaPortDown(r) {
			reasons = append(reasons, ReasonRDMAPortDown)
		}
		if r.RDMA.KernelModules != nil && !r.RDMA.KernelModules["nvidia_peermem"] {
			reasons = append(reasons, ReasonRDMAPeermemMissing)
		}
		if rdmaCollectorStale(r, now) {
			reasons = append(reasons, ReasonRDMACollectorStale)
		}
	}
	if r.Mode == "training_mode" {
		reasons = append(reasons, ReasonTrainingInProgress)
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
	if !ollamaDisabled && runnerStuck(r) {
		reasons = append(reasons, ReasonOllamaRunnerStuck)
	}
	if serviceCreep(r, creepWarn) {
		reasons = append(reasons, ReasonVRAMServiceCreepWarn)
	}
	if maxDiskPct(r) > 90 && maxDiskPct(r) <= 98 {
		reasons = append(reasons, ReasonDiskOver90pct)
	}
	if clockSkewHigh(r) {
		reasons = append(reasons, ReasonClockSkewHigh)
	}
	if r.CPU.Throttled != nil && *r.CPU.Throttled {
		reasons = append(reasons, ReasonCPUThermalThrottling)
	}
	if anyGPUThrottle(r, "THERMAL") {
		reasons = append(reasons, ReasonGPUThermalThrottling)
	}
	if anyGPUThrottle(r, "POWER") {
		reasons = append(reasons, ReasonGPUPowerThrottling)
	}
	if vllmSoftDown(r, cfg) {
		reasons = append(reasons, ReasonVLLMDown)
	}
	if r.RDMA != nil {
		if rdmaLinkDegraded(r) {
			reasons = append(reasons, ReasonRDMALinkDegraded)
		}
	}

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
// On unified-memory hosts (Apple Silicon, NVIDIA GB10) the GPU's VRAM
// ceiling and usage are back-filled from system memory in
// applyUnifiedMemoryDerivation, so this function returns a real percentage
// rather than 0 and vram_over_*pct can fire normally.
func maxVRAMPct(r Report) float64 {
	var m float64
	for _, g := range r.GPUs {
		if g.VRAMUsedPct > m {
			m = g.VRAMUsedPct
		}
	}
	return m
}

func maxDiskPct(r Report) float64 {
	var m float64
	for _, d := range r.Disk {
		if d.UsedPct > m {
			m = d.UsedPct
		}
	}
	return m
}

func hasUncorrectableECC(r Report) bool {
	for _, g := range r.GPUs {
		if g.ECCVolatileUncorrected != nil && *g.ECCVolatileUncorrected > 0 {
			return true
		}
	}
	return false
}

func anyGPUThrottle(r Report, kind string) bool {
	for _, g := range r.GPUs {
		for _, reason := range g.ThrottleReasons {
			if kind == "THERMAL" && (reason == "HW_THERMAL_SLOWDOWN" || reason == "SW_THERMAL_SLOWDOWN") {
				return true
			}
			if kind == "POWER" && (reason == "HW_POWER_BRAKE_SLOWDOWN" || reason == "SW_POWER_CAP") {
				return true
			}
		}
	}
	return false
}

func clockSkewHigh(r Report) bool {
	if r.TimeSync == nil || r.TimeSync.SkewMS == nil {
		return false
	}
	v := *r.TimeSync.SkewMS
	if v < 0 {
		v = -v
	}
	return v > 100
}

// vllmRequiredDown fires when config says vllm is required but the platforms
// probe says it's not up.
func vllmRequiredDown(r Report, cfg config.Config) bool {
	if !cfg.Platforms.VLLM.Required {
		return false
	}
	p, ok := r.Platforms["vllm"]
	return ok && !p.Up
}

// vllmSoftDown fires when vllm is configured (enabled != "false") but down,
// and isn't already covered by the hard `vllm_required_down` case.
func vllmSoftDown(r Report, cfg config.Config) bool {
	if cfg.Platforms.VLLM.Required {
		return false // covered by vllm_required_down (hard)
	}
	// Only fire when the operator explicitly enabled vLLM monitoring.
	//   "true"   → fire on failure (operator wants to be told)
	//   "auto"   → best-effort detection; do NOT fire on failure
	//   "false"  → don't probe at all
	// Pre-v0.2.8 "auto" fired vllm_down — which flooded every Ollama-only
	// host with the noise (see issue #11). The "auto" semantic is now
	// "probe and report state, but don't escalate to degraded_reasons"
	// — consumers who care read platforms.vllm.up directly.
	if cfg.Platforms.VLLM.Enabled != "true" {
		return false
	}
	p, ok := r.Platforms["vllm"]
	if !ok {
		return false
	}
	// Only fire if the agent actually attempted a probe. Empty endpoint
	// means platforms.vllm wasn't configured.
	if p.Endpoint == "" {
		return false
	}
	return !p.Up
}

// rdmaPortDown fires when any RDMA port isn't ACTIVE / LINK_UP. Training
// dispatch reads this directly to refuse a node mid-training.
func rdmaPortDown(r Report) bool {
	for _, d := range r.RDMA.Devices {
		if d.State != "ACTIVE" {
			return true
		}
		if d.PhysicalState != "" && d.PhysicalState != "LINK_UP" {
			return true
		}
	}
	return false
}

// rdmaCollectorStale fires when LastCollectedTS is older than 30s on any
// device. Indicates the agent's collection loop is unhealthy and the
// dispatcher should not trust the rest of the rdma block.
func rdmaCollectorStale(r Report, now time.Time) bool {
	for _, d := range r.RDMA.Devices {
		if d.LastCollectedTS == 0 {
			continue
		}
		if now.Unix()-d.LastCollectedTS > 30 {
			return true
		}
	}
	return false
}

// rdmaLinkDegraded fires when an active port reports a rate below 200 Gbps.
// 200 G is the Spark/CX-7 baseline; lower indicates a misconfiguration
// (wrong cable, auto-negotiation glitch). Skipped for ports that don't
// expose a rate (some older drivers leave the file blank).
func rdmaLinkDegraded(r Report) bool {
	for _, d := range r.RDMA.Devices {
		if d.State == "ACTIVE" && d.RateGbps > 0 && d.RateGbps < 200 {
			return true
		}
	}
	return false
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
// AND at least one model reports queued_requests > 0. Historically v1
// approximated the second clause with "any model loaded," which falsely
// flagged every warm idle runner (see issue #1). Now we require real
// queue-depth evidence before firing.
//
// Ollama versions that don't expose queued_requests in /api/ps leave the
// field at 0, which means the check never fires — deliberate: better to
// under-detect than to false-positive and deprioritize healthy nodes.
func runnerStuck(r Report) bool {
	if len(r.Ollama.Runners) == 0 {
		return false
	}
	queued := 0
	for _, m := range r.Ollama.Models {
		queued += m.QueuedRequests
	}
	if queued == 0 {
		return false
	}
	for _, rn := range r.Ollama.Runners {
		if rn.CPUPct < 0.5 {
			return true
		}
	}
	return false
}
