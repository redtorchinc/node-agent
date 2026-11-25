package health

import (
	"strconv"
	"sync"
	"time"

	"github.com/redtorchinc/node-agent/internal/gpu"
)

// gpu_underclocked (#54): a GPU that is busy but running far below its normal
// clock. Seen on a GB10 that came back from a reboot at 611 MHz / 8 W against a
// 2418 MHz applications clock, serving at about half speed for 29 h. The driver
// set no clocks_event_reasons bit (P0, mask 0x0), so gpu_*_throttling stayed
// silent and the node reported healthy. This rule reads the behaviour instead.
const (
	underclockUtilMinPct = 50              // busy: idle GPUs downclock by design
	underclockClockRatio = 0.5             // under half the reference clock
	underclockWindow     = 5 * time.Minute // sustained, not a single dip
)

// underclockedNow reports whether a GPU is busy at under half its reference
// clock. The reference is the applications clock, else the max graphics clock.
// Silent when a clock is unreported: never fire from a missing metric.
func underclockedNow(g gpu.GPU) bool {
	ref := g.ClockGraphicsAppMHz
	if ref <= 0 {
		ref = g.ClockGraphicsMaxMHz
	}
	clk := g.ClockSMMHz
	if clk <= 0 {
		clk = g.ClockGraphicsMHz
	}
	if ref <= 0 || clk <= 0 {
		return false
	}
	return g.UtilPct >= underclockUtilMinPct && float64(clk) < underclockClockRatio*float64(ref)
}

// underclockTracker remembers, per GPU, when its current unbroken underclocked
// run began, so Evaluate (a pure function of one Report) can apply the window.
// Safe for concurrent Report() calls; the zero value is ready to use.
type underclockTracker struct {
	mu    sync.Mutex
	since map[string]int64
}

// stamp sets UnderclockedSince on every GPU in the state now, keeping the
// first-seen time across samples. A GPU that leaves the state, or disappears,
// restarts its run.
func (t *underclockTracker) stamp(gpus []gpu.GPU, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.since == nil {
		t.since = map[string]int64{}
	}
	seen := make(map[string]bool, len(gpus))
	for i := range gpus {
		k := gpuKey(gpus[i])
		seen[k] = true
		if !underclockedNow(gpus[i]) {
			delete(t.since, k)
			continue
		}
		s, ok := t.since[k]
		if !ok {
			s = now.Unix()
			t.since[k] = s
		}
		gpus[i].UnderclockedSince = s
	}
	for k := range t.since {
		if !seen[k] {
			delete(t.since, k)
		}
	}
}

func gpuKey(g gpu.GPU) string {
	if g.UUID != "" {
		return g.UUID
	}
	return strconv.Itoa(g.Index)
}

// gpuUnderclocked fires once any GPU has been underclocked for the full window.
func gpuUnderclocked(r Report, now time.Time) bool {
	for _, g := range r.GPUs {
		if g.UnderclockedSince > 0 && now.Unix()-g.UnderclockedSince >= int64(underclockWindow/time.Second) {
			return true
		}
	}
	return false
}
