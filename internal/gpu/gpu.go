// Package gpu enumerates GPUs and the processes using them.
//
// Each platform has its own probe (nvidia-smi on Linux/Windows/Intel-Mac,
// ioreg+sysctl on Apple Silicon). A Noop probe is used when no GPU stack
// is available. The active Probe is selected at runtime by Select().
package gpu

import "context"

// GPU mirrors the /health JSON shape from SPEC.md §HTTP API.
type GPU struct {
	Index       int       `json:"index"`
	Name        string    `json:"name"`
	VRAMTotalMB int64     `json:"vram_total_mb"`
	VRAMUsedMB  int64     `json:"vram_used_mb"`
	VRAMUsedPct float64   `json:"vram_used_pct"`
	UtilPct     int       `json:"util_pct"`
	TempC       int       `json:"temp_c"`
	PowerW      int       `json:"power_w"`
	PowerCapW   int       `json:"power_cap_w"`
	Processes   []Process `json:"processes"`
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
