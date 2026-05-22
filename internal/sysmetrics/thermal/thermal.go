// Package thermal surfaces additional temperature sensors on platforms
// where gopsutil's host.SensorsTemperatures() returns nothing useful.
//
// Today this means darwin: gopsutil's macOS path reads the legacy IOKit
// SMC keys (TC0P, TG0P, …) which return empty on Apple Silicon — Apple
// moved sensor access behind AppleSPMI / IOHID after the M1 transition.
// We shell out to /usr/bin/powermetrics --samplers smc, which Apple
// itself ships and which exposes real die temps as long as the caller
// is root. The agent runs as root via launchd, so this works in
// production.
//
// The probe runs in a 15s background loop so the powermetrics shell-out
// (~1s wall, blocking on its own sample interval) never sits on the
// /health request path. On non-darwin platforms Start is a no-op and
// Snapshot returns the zero value, which the health composer reads as
// "absent" — matching the SPEC.md rule that a missing metric must never
// be fabricated.
package thermal

import (
	"context"
	"sync"
	"time"
)

// refreshInterval is how often the background goroutine re-runs the
// platform-specific probe. 15s is long enough that the powermetrics cost
// is negligible amortized, and short enough that the case-manager's
// ~30s /health poll always sees a reading <30s old.
const refreshInterval = 15 * time.Second

// Snapshot is the most recent cached reading. HaveCPU / HaveGPU gate
// whether the corresponding value is meaningful; consumers must check
// the boolean before reading the float.
type Snapshot struct {
	CPUDieC float64
	GPUDieC float64
	HaveCPU bool
	HaveGPU bool
	LastTS  time.Time

	// ProbeIntervalS is the cadence the background goroutine refreshes
	// at. Surfaced so future /health fields can self-describe staleness
	// the same way platforms.* already does.
	ProbeIntervalS int64
}

// Probe runs a background refresh loop and serves the latest reading.
type Probe struct {
	mu   sync.RWMutex
	snap Snapshot
}

// New returns a Probe whose Snapshot is initially empty. Call Start to
// launch the background refresh.
func New() *Probe {
	return &Probe{
		snap: Snapshot{ProbeIntervalS: int64(refreshInterval / time.Second)},
	}
}

// Snapshot returns the most recent reading. Safe for concurrent use.
// Returns a zero-value Snapshot (HaveCPU=HaveGPU=false) until the first
// successful refresh completes.
func (p *Probe) Snapshot() Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.snap
}

// Start launches the platform-specific refresh loop. Stops on ctx
// cancellation. Safe to call once per Probe; calling more than once
// will leak goroutines.
func (p *Probe) Start(ctx context.Context) { p.start(ctx) }

// store overwrites the cached snapshot. Called by platform-specific
// refresh functions.
func (p *Probe) store(s Snapshot) {
	s.ProbeIntervalS = int64(refreshInterval / time.Second)
	p.mu.Lock()
	p.snap = s
	p.mu.Unlock()
}
