package health

import (
	"testing"
	"time"

	"github.com/redtorchinc/node-agent/internal/config"
	"github.com/redtorchinc/node-agent/internal/gpu"
)

// The #54 incident: a GB10 busy at 611 MHz against a 2418 MHz applications
// clock, 8 W, P0, no throttle bit set.
func stuckGB10() gpu.GPU {
	return gpu.GPU{UUID: "GPU-stuck", UtilPct: 74, PowerW: 8,
		ClockSMMHz: 611, ClockGraphicsMHz: 611, ClockGraphicsMaxMHz: 3003, ClockGraphicsAppMHz: 2418}
}

// Its healthy peer on the same model and load.
func healthyGB10() gpu.GPU {
	return gpu.GPU{UUID: "GPU-ok", UtilPct: 96, PowerW: 64,
		ClockSMMHz: 2437, ClockGraphicsMHz: 2437, ClockGraphicsMaxMHz: 3003, ClockGraphicsAppMHz: 2418}
}

func TestUnderclockedNow(t *testing.T) {
	idle := stuckGB10()
	idle.UtilPct = 3
	noApp := stuckGB10()
	noApp.ClockGraphicsAppMHz = 0 // falls back to the max clock
	noClocks := stuckGB10()
	noClocks.ClockSMMHz, noClocks.ClockGraphicsMHz = 0, 0
	apple := gpu.GPU{UtilPct: 90, VRAMUnified: true} // no clocks reported

	cases := []struct {
		name string
		g    gpu.GPU
		want bool
	}{
		{"stuck and busy", stuckGB10(), true},
		{"healthy and busy", healthyGB10(), false},
		{"low clock at idle is normal", idle, false},
		{"no applications clock uses the max clock", noApp, true},
		{"unreported clock never fires", noClocks, false},
		{"Apple GPU without clocks", apple, false},
	}
	for _, c := range cases {
		if got := underclockedNow(c.g); got != c.want {
			t.Errorf("%s: underclockedNow = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestUnderclockTracker_KeepsOnsetAndResetsOnRecovery(t *testing.T) {
	var tr underclockTracker
	t0 := time.Unix(1795600000, 0)

	gpus := []gpu.GPU{stuckGB10(), healthyGB10()}
	tr.stamp(gpus, t0)
	if gpus[0].UnderclockedSince != t0.Unix() {
		t.Fatalf("first sample: since = %d, want %d", gpus[0].UnderclockedSince, t0.Unix())
	}
	if gpus[1].UnderclockedSince != 0 {
		t.Fatalf("healthy GPU stamped: %d", gpus[1].UnderclockedSince)
	}

	// A later sample keeps the original onset (fresh structs, as the probe returns).
	gpus = []gpu.GPU{stuckGB10(), healthyGB10()}
	tr.stamp(gpus, t0.Add(3*time.Minute))
	if gpus[0].UnderclockedSince != t0.Unix() {
		t.Fatalf("onset moved: %d", gpus[0].UnderclockedSince)
	}

	// Recovery clears it; going bad again starts a new run.
	gpus = []gpu.GPU{healthyGB10()}
	gpus[0].UUID = "GPU-stuck"
	tr.stamp(gpus, t0.Add(4*time.Minute))
	gpus = []gpu.GPU{stuckGB10()}
	tr.stamp(gpus, t0.Add(10*time.Minute))
	if want := t0.Add(10 * time.Minute).Unix(); gpus[0].UnderclockedSince != want {
		t.Fatalf("after recovery: since = %d, want %d", gpus[0].UnderclockedSince, want)
	}
}

func TestEvaluate_GPUUnderclocked_FiresAfterWindowAsSoft(t *testing.T) {
	now := time.Unix(1795600000, 0)

	r := cleanReport(now)
	g := stuckGB10()
	g.UnderclockedSince = now.Add(-underclockWindow + time.Second).Unix()
	r.GPUs = []gpu.GPU{g}
	if _, reasons := Evaluate(r, config.Config{}, now); contains(reasons, ReasonGPUUnderclocked) {
		t.Fatalf("fired before the window: %v", reasons)
	}

	r.GPUs[0].UnderclockedSince = now.Add(-underclockWindow).Unix()
	deg, reasons := Evaluate(r, config.Config{}, now)
	if !contains(reasons, ReasonGPUUnderclocked) {
		t.Fatalf("did not fire after the window: %v", reasons)
	}
	if deg {
		t.Errorf("gpu_underclocked must be soft, got hard degraded")
	}
	if !hasSoftReason(reasons) {
		t.Errorf("hasSoftReason = false for %v", reasons)
	}
}

func TestEvaluate_GPUUnderclocked_SilentForHealthyGPU(t *testing.T) {
	now := time.Unix(1795600000, 0)
	r := cleanReport(now)
	r.GPUs = []gpu.GPU{healthyGB10()}
	if _, reasons := Evaluate(r, config.Config{}, now); contains(reasons, ReasonGPUUnderclocked) {
		t.Fatalf("fired on a healthy GPU: %v", reasons)
	}
}
