package gpu

import "time"

// probeCacheTTL bounds /health p99 by making repeated calls cheap. The
// underlying probes (nvidia-smi, system_profiler) can spike to 1-2s under
// host load, which violates the case-manager's 2s client timeout on
// /health.
//
// 30s is sized to outlast the case-manager's own 30s response cache —
// otherwise every backend poll catches our cache cold and pays the full
// system_profiler / nvidia-smi cost. A keep-warm ticker in
// internal/health/StartBackground refreshes the cache every ~25s so
// idle agents stay warm too.
//
// Numbers that matter for degraded_reasons (vram_over_95pct and friends)
// don't flip within 30s in practice — VRAM shifts by tens of MB during
// active inference, not by entire utilisation tiers.
const probeCacheTTL = 30 * time.Second

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
