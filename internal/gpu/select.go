package gpu

import "time"

// probeCacheTTL bounds /health p99 by making repeated calls cheap. The
// underlying probes (nvidia-smi, system_profiler) can spike to 1-2s under
// host load, which violates the case-manager's 2s client timeout on
// /health. 5s is chosen as a compromise:
//   - short enough that VRAM util / temp / power numbers stay close to
//     real-time, so the ranker doesn't make decisions from stale data
//   - long enough that a burst of backend calls (multiple workers hitting
//     the same node) collapses to a single subprocess invocation
//
// Numbers that matter for degraded_reasons (vram_over_95pct and friends)
// don't flip within 5s in practice — VRAM shifts by a few MB, not by
// thresholds.
const probeCacheTTL = 5 * time.Second

// Select returns a cached Probe appropriate for the current host. On
// darwin/arm64 the Apple Silicon probe is preferred; on any OS where
// nvidia-smi is on PATH the NvidiaSMI probe is used; otherwise Noop.
// All selections are wrapped in a CachedProbe so /health latency is
// bounded by cache reads for repeated calls.
func Select() Probe {
	return NewCached(selectInner(), probeCacheTTL)
}

func selectInner() Probe {
	if p, ok := selectPlatform(); ok {
		return p
	}
	n := NewNvidiaSMI()
	if n.Available() {
		return n
	}
	return NewNoop()
}
