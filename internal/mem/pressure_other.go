//go:build !linux && !darwin

package mem

// probePressure is a no-op on platforms without a native PSI-equivalent.
// Windows `GetMemoryStatusEx` integration is on the v0.3 roadmap;
// returning "" surfaces the field as absent rather than fabricated.
func probePressure() string { return "" }

// probePSI is Linux-only (PSI is a Linux kernel feature). Returns nil so
// the composer leaves the raw gauges absent on non-Linux platforms.
func probePSI() *PSI { return nil }

// probeSwapCounters is Linux-only on this build target. Returns
// (0,0,false) so the composer leaves swap_in/out_pages_total absent.
func probeSwapCounters() (uint64, uint64, bool) { return 0, 0, false }

// probeHugePages is Linux-only.
func probeHugePages() (int64, int64, bool) { return 0, 0, false }
