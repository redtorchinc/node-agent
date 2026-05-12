package server

import (
	"net/http"
	"runtime"

	"github.com/redtorchinc/node-agent/internal/buildinfo"
	"github.com/redtorchinc/node-agent/internal/config"
)

// Capabilities mirrors V0_2_0_PLAN.md §A4.7. It's the dispatcher's
// feature-detection oracle — read it once per node, key off the field set
// to decide whether to rely on platforms/services/training-mode/etc.
//
// Read-only, no auth (LAN open) — same trust posture as /health.
type Capabilities struct {
	AgentVersion                  string   `json:"agent_version"`
	ConfigVersion                 int      `json:"config_version"`
	OS                            string   `json:"os"`
	Arch                          string   `json:"arch"`
	PlatformsSupported            []string `json:"platforms_supported"`
	ActionsSupported              []string `json:"actions_supported"`
	ServicesAllowlist             []string `json:"services_allowlist"`
	RDMAAvailable                 bool     `json:"rdma_available"`
	TrainingModeSupported         bool     `json:"training_mode_supported"`
	MetricsEnabled                bool     `json:"metrics_enabled"`
	SystemMetricsFieldsSupported  []string `json:"system_metrics_fields_supported"`
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c := Capabilities{
		AgentVersion:       buildinfo.Version,
		ConfigVersion:      config.SchemaVersion,
		OS:                 runtime.GOOS,
		Arch:               runtime.GOARCH,
		PlatformsSupported: []string{"ollama", "vllm"},
		ActionsSupported:   []string{"unload-model", "service", "training-mode"},
		MetricsEnabled:     s.cfg.MetricsEnabled,
		TrainingModeSupported: true,
	}
	// RDMA presence is detected at probe time; the agent advertises support
	// when the package's runtime check passes. On non-Linux we always
	// surface false.
	c.RDMAAvailable = rdmaAvailable()

	if s.svcMgr != nil {
		for _, e := range s.svcMgr.Capabilities() {
			c.ServicesAllowlist = append(c.ServicesAllowlist, e.Name)
		}
	}
	if c.ServicesAllowlist == nil {
		c.ServicesAllowlist = []string{}
	}
	c.SystemMetricsFieldsSupported = systemMetricsFieldList()

	writeJSON(w, http.StatusOK, c)
}

// rdmaAvailable returns true on Linux DGX-class boxes only. Implementation
// is in rdma_avail_{linux,other}.go to keep this file build-tag-free.
//
// Defined here as a thin wrapper so the server package isn't directly
// importing internal/rdma until that lands; the per-OS stub returns false
// pre-B1 and the linux file in internal/rdma re-exposes Available() once
// B1 is in.

// systemMetricsFieldList describes what /health.cpu and /health.gpus this
// build of the agent populates on this OS/arch. Dispatchers rank node
// types using this list rather than parsing semver.
func systemMetricsFieldList() []string {
	out := []string{
		"cpu.cores_physical",
		"cpu.cores_logical",
		"cpu.usage_pct",
		"cpu.usage_per_core_pct",
		"cpu.model",
		"cpu.vendor",
		"memory.total_mb",
		"memory.used_mb",
		"memory.unified",
		"memory.swap_used_pct",
		"gpu.vram_used_pct",
		"gpu.processes",
		"disk.used_pct",
		"network.interfaces",
	}
	if runtime.GOOS != "windows" {
		out = append(out, "cpu.load_1m", "cpu.load_5m", "cpu.load_15m")
	}
	if runtime.GOOS == "linux" {
		out = append(out, "time_sync.skew_ms")
	}
	return out
}
