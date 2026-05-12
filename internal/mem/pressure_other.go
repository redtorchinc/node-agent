//go:build !linux

package mem

// probePressure is a no-op on non-Linux platforms in v0.2.0.
// macOS `memory_pressure` and Windows `GetMemoryStatusEx` parsing are on
// the v0.3 roadmap; returning "" surfaces the field as absent rather than
// fabricated.
func probePressure() string { return "" }

// probeHugePages is Linux-only.
func probeHugePages() (int64, int64, bool) { return 0, 0, false }
