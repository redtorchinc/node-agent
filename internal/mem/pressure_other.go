//go:build !linux

package mem

// probePressure is a no-op on non-Linux platforms in v0.2.0.
// macOS `memory_pressure` and Windows `GetMemoryStatusEx` parsing are on
// the v0.3 roadmap; returning "" surfaces the field as absent rather than
// fabricated.
func probePressure() string { return "" }

// probePSI is Linux-only (PSI is a Linux kernel feature). Returns nil so
// the composer leaves the raw gauges absent on non-Linux platforms.
func probePSI() *PSI { return nil }

// probeSwapCounters is Linux-only. Returns (0,0,false) so the composer
// leaves swap_in/out_pages_total absent on non-Linux platforms. macOS
// reports swap-in/out via Mach VM stats but the units don't match Linux's
// page counters; surfacing them under the same name would be wrong.
func probeSwapCounters() (uint64, uint64, bool) { return 0, 0, false }

// probeHugePages is Linux-only.
func probeHugePages() (int64, int64, bool) { return 0, 0, false }
