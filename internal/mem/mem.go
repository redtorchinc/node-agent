// Package mem reports RAM and swap usage. On Apple Silicon the Info struct
// has Unified=true to tell the ranker that RAM pressure implies GPU
// pressure (there is no separate VRAM pool).
package mem

import (
	"context"

	"github.com/shirou/gopsutil/v3/mem"
)

// Info mirrors the /health JSON shape from SPEC.md §HTTP API.
type Info struct {
	TotalMB     int64   `json:"total_mb"`
	UsedMB      int64   `json:"used_mb"`
	UsedPct     float64 `json:"used_pct"`
	SwapTotalMB int64   `json:"swap_total_mb"`
	SwapUsedMB  int64   `json:"swap_used_mb"`
	SwapUsedPct float64 `json:"swap_used_pct"`
	Unified     bool    `json:"unified"`
}

// Probe collects RAM and swap stats. Blocks for gopsutil-internal I/O but
// should return in well under a second on any supported platform.
func Probe(_ context.Context) (Info, error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return Info{}, err
	}
	s, err := mem.SwapMemory()
	if err != nil {
		return Info{}, err
	}
	i := Info{
		TotalMB:     int64(v.Total / 1024 / 1024),
		UsedMB:      int64(v.Used / 1024 / 1024),
		UsedPct:     round2(v.UsedPercent),
		SwapTotalMB: int64(s.Total / 1024 / 1024),
		SwapUsedMB:  int64(s.Used / 1024 / 1024),
		SwapUsedPct: round2(s.UsedPercent),
		Unified:     unified(),
	}
	return i, nil
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
