// Package gpu enumerates GPUs and the processes using them.
//
// Each platform has its own probe (nvidia-smi on Linux/Windows/Intel-Mac,
// ioreg+sysctl on Apple Silicon). A Noop probe is used when no GPU stack
// is available. The active Probe is selected at runtime by Select().
package gpu

import "context"

// GPU mirrors the /health JSON shape from SPEC.md §HTTP API. v0.2.0 adds
// driver_version, clocks, throttle reasons, ECC, MIG and NVLink fields
// (all `omitempty` so v0.1.x backends ignore them safely).
type GPU struct {
	Index             int       `json:"index"`
	UUID              string    `json:"uuid,omitempty"`
	Name              string    `json:"name"`
	DriverVersion     string    `json:"driver_version,omitempty"`
	CUDAVersion       string    `json:"cuda_version,omitempty"`
	ComputeCapability string    `json:"compute_capability,omitempty"`
	PCIBusID          string    `json:"pci_bus_id,omitempty"`

	VRAMTotalMB int64   `json:"vram_total_mb"`
	VRAMUsedMB  int64   `json:"vram_used_mb"`
	VRAMUsedPct float64 `json:"vram_used_pct"`

	// VRAMUnified is true only on Apple Silicon. The ranker reads this to
	// know that vram_total_mb == 0 isn't a probe failure but a unified-memory
	// platform; in that case memory.used_pct is the right signal.
	VRAMUnified bool `json:"vram_unified,omitempty"`

	UtilPct       int `json:"util_pct"`
	MemoryUtilPct int `json:"memory_util_pct,omitempty"`

	TempC       int `json:"temp_c"`
	TempMemoryC int `json:"temp_memory_c,omitempty"`

	PowerW    int `json:"power_w"`
	PowerCapW int `json:"power_cap_w"`

	ClockGraphicsMHz    int `json:"clock_graphics_mhz,omitempty"`
	ClockMemoryMHz      int `json:"clock_memory_mhz,omitempty"`
	ClockSMMHz          int `json:"clock_sm_mhz,omitempty"`
	ClockGraphicsMaxMHz int `json:"clock_graphics_max_mhz,omitempty"`

	ThrottleReasons []string `json:"throttle_reasons,omitempty"`

	ECCVolatileUncorrected  *int64 `json:"ecc_volatile_uncorrected,omitempty"`
	ECCAggregateUncorrected *int64 `json:"ecc_aggregate_uncorrected,omitempty"`

	FanPct          *int   `json:"fan_pct,omitempty"`
	PersistenceMode string `json:"persistence_mode,omitempty"`
	ComputeMode     string `json:"compute_mode,omitempty"`
	MIGMode         string `json:"mig_mode,omitempty"`

	NVLink *NVLink `json:"nvlink,omitempty"`

	Processes []Process `json:"processes"`
}

// NVLink describes the inter-GPU NVLink fabric.
type NVLink struct {
	Supported bool        `json:"supported"`
	Links     []NVLinkLink `json:"links,omitempty"`
}

// NVLinkLink is one physical link.
type NVLinkLink struct {
	Link        int    `json:"link"`
	State       string `json:"state"`            // Up | Down
	SpeedGBPerS int    `json:"speed_gb_s,omitempty"`
	PeerGPU     *int   `json:"peer_gpu_index,omitempty"`
}

// Process is a single consumer of VRAM on a GPU.
type Process struct {
	PID         int    `json:"pid"`
	Name        string `json:"name"`
	CmdlineHead string `json:"cmdline_head"`
	VRAMUsedMB  int64  `json:"vram_used_mb"`
}

// Probe reports the current GPU inventory. Implementations must be safe
// for concurrent use and return quickly enough to fit inside a /health
// response (the caller applies its own deadline via ctx).
type Probe interface {
	Probe(ctx context.Context) ([]GPU, error)
}
