// Package mem reports RAM and swap usage. On Apple Silicon the Info struct
// has Unified=true to tell the ranker that RAM pressure implies GPU
// pressure (there is no separate VRAM pool).
package mem

import (
	"context"

	gmem "github.com/shirou/gopsutil/v3/mem"
)

// Info mirrors the /health JSON shape from SPEC.md §HTTP API. v0.2.0 adds
// AvailableMB/BuffersMB/CachedMB/Pressure/HugePages — additive only.
// v0.2.3 adds raw swap counters (/proc/vmstat pswpin/pswpout) and raw PSI
// gauges so the backend can compute swap-page rates and pressure trends
// without inheriting the agent's classification.
type Info struct {
	TotalMB     int64   `json:"total_mb"`
	UsedMB      int64   `json:"used_mb"`
	UsedPct     float64 `json:"used_pct"`
	AvailableMB int64   `json:"available_mb,omitempty"`
	BuffersMB   int64   `json:"buffers_mb,omitempty"`
	CachedMB    int64   `json:"cached_mb,omitempty"`
	SwapTotalMB int64   `json:"swap_total_mb"`
	SwapUsedMB  int64   `json:"swap_used_mb"`
	SwapUsedPct float64 `json:"swap_used_pct"`
	Unified     bool    `json:"unified"`

	// SwapInPagesTotal / SwapOutPagesTotal are cumulative kernel counters
	// (Linux /proc/vmstat pswpin / pswpout). Use rate() across two scrapes
	// for "pages swapped/sec". Pointer so a kernel without these counters
	// (BSD, older Linux) emits absent rather than a fabricated zero.
	SwapInPagesTotal  *uint64 `json:"swap_in_pages_total,omitempty"`
	SwapOutPagesTotal *uint64 `json:"swap_out_pages_total,omitempty"`

	// Pressure is platform-specific text: "normal" | "some" | "full" on
	// Linux PSI-capable kernels, "normal" | "warn" | "critical" on macOS,
	// "low" | "high" on Windows. Omitted when unavailable.
	Pressure string `json:"pressure,omitempty"`

	// PressureSomeAvg10 / PressureSomeAvg60 / PressureFullAvg10 /
	// PressureFullAvg60 expose raw PSI gauges from /proc/pressure/memory.
	// Linux >= 4.20 with PSI enabled. The ranker may want avg60 (60s smoothed)
	// rather than the classification; both are now available. Omitted on
	// platforms without PSI.
	PressureSomeAvg10 *float64 `json:"pressure_some_avg10,omitempty"`
	PressureSomeAvg60 *float64 `json:"pressure_some_avg60,omitempty"`
	PressureFullAvg10 *float64 `json:"pressure_full_avg10,omitempty"`
	PressureFullAvg60 *float64 `json:"pressure_full_avg60,omitempty"`

	// HugePagesTotal/Free are Linux-only.
	HugePagesTotal *int64 `json:"huge_pages_total,omitempty"`
	HugePagesFree  *int64 `json:"huge_pages_free,omitempty"`
}

// Probe collects RAM and swap stats. Blocks for gopsutil-internal I/O but
// should return in well under a second on any supported platform.
func Probe(_ context.Context) (Info, error) {
	v, err := gmem.VirtualMemory()
	if err != nil {
		return Info{}, err
	}
	s, err := gmem.SwapMemory()
	if err != nil {
		return Info{}, err
	}
	i := Info{
		TotalMB:     int64(v.Total / 1024 / 1024),
		UsedMB:      int64(v.Used / 1024 / 1024),
		UsedPct:     round2(v.UsedPercent),
		AvailableMB: int64(v.Available / 1024 / 1024),
		BuffersMB:   int64(v.Buffers / 1024 / 1024),
		CachedMB:    int64(v.Cached / 1024 / 1024),
		SwapTotalMB: int64(s.Total / 1024 / 1024),
		SwapUsedMB:  int64(s.Used / 1024 / 1024),
		SwapUsedPct: round2(s.UsedPercent),
		Unified:     unified(),
	}
	if psi := probePSI(); psi != nil {
		i.Pressure = psi.Classification
		some10 := psi.SomeAvg10
		some60 := psi.SomeAvg60
		full10 := psi.FullAvg10
		full60 := psi.FullAvg60
		i.PressureSomeAvg10 = &some10
		i.PressureSomeAvg60 = &some60
		i.PressureFullAvg10 = &full10
		i.PressureFullAvg60 = &full60
	}
	if pIn, pOut, ok := probeSwapCounters(); ok {
		i.SwapInPagesTotal = &pIn
		i.SwapOutPagesTotal = &pOut
	}
	if total, free, ok := probeHugePages(); ok {
		i.HugePagesTotal = &total
		i.HugePagesFree = &free
	}
	return i, nil
}

// PSI holds the raw values pulled from /proc/pressure/memory. Returned by
// probePSI on Linux; nil elsewhere.
type PSI struct {
	Classification string // "normal" | "some" | "full"
	SomeAvg10      float64
	SomeAvg60      float64
	FullAvg10      float64
	FullAvg60      float64
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
