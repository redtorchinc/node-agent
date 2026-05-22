package health

import (
	"testing"

	"github.com/redtorchinc/node-agent/internal/gpu"
	"github.com/redtorchinc/node-agent/internal/sysmetrics/thermal"
)

// Apple Silicon: the GPU probe (system_profiler) reports TempC=0
// because there's no public API for it. The thermal overlay must
// populate temp_c from the cached powermetrics reading, and append a
// cpu_die TempSensor to /health.cpu.temps_c.
func TestApplyThermalOverlay_AppleSilicon(t *testing.T) {
	rep := Report{
		CPU: CPUInfo{},
		GPUs: []gpu.GPU{{
			Name:        "Apple M2 Ultra",
			VRAMUnified: true,
			TempC:       0,
		}},
	}
	applyThermalOverlay(&rep, thermal.Snapshot{
		CPUDieC: 49.66,
		GPUDieC: 42.42,
		HaveCPU: true,
		HaveGPU: true,
	})
	if len(rep.CPU.TempsC) != 1 || rep.CPU.TempsC[0].Sensor != "cpu_die" || rep.CPU.TempsC[0].Value != 49.66 {
		t.Errorf("CPU.TempsC = %+v, want one cpu_die @ 49.66", rep.CPU.TempsC)
	}
	if rep.GPUs[0].TempC != 42 {
		t.Errorf("GPUs[0].TempC = %d, want 42 (rounded from 42.42)", rep.GPUs[0].TempC)
	}
}

// Discrete NVIDIA on Linux: nvidia-smi has already populated temp_c.
// The thermal overlay must not clobber it. (In practice the thermal
// probe is a no-op on linux today, but the guard belongs in the
// overlay so future hwmon-based readings can coexist safely.)
func TestApplyThermalOverlay_DoesNotClobberDiscreteGPUTemp(t *testing.T) {
	rep := Report{
		GPUs: []gpu.GPU{{
			Name:        "NVIDIA H100 80GB HBM3",
			VRAMUnified: false,
			TempC:       71,
		}},
	}
	applyThermalOverlay(&rep, thermal.Snapshot{
		GPUDieC: 99.99,
		HaveGPU: true,
	})
	if rep.GPUs[0].TempC != 71 {
		t.Errorf("discrete NVIDIA TempC overwritten: got %d, want 71", rep.GPUs[0].TempC)
	}
}

// Snapshot says HaveCPU=false / HaveGPU=false (cold start, or non-root,
// or non-darwin): overlay must be a no-op.
func TestApplyThermalOverlay_EmptySnapshot(t *testing.T) {
	rep := Report{
		CPU:  CPUInfo{TempsC: []TempSensor{{Sensor: "preexisting", Value: 50}}},
		GPUs: []gpu.GPU{{Name: "Apple M3", VRAMUnified: true, TempC: 0}},
	}
	applyThermalOverlay(&rep, thermal.Snapshot{})
	if len(rep.CPU.TempsC) != 1 || rep.CPU.TempsC[0].Sensor != "preexisting" {
		t.Errorf("CPU.TempsC mutated on empty snapshot: %+v", rep.CPU.TempsC)
	}
	if rep.GPUs[0].TempC != 0 {
		t.Errorf("GPU.TempC mutated on empty snapshot: got %d, want 0", rep.GPUs[0].TempC)
	}
}

// gopsutil populated temps_c on (say) Linux hwmon. The cpu_die entry
// from the thermal probe must be appended, not replace the gopsutil
// readings — operators want to see both sources.
func TestApplyThermalOverlay_AppendsToExistingTempsC(t *testing.T) {
	rep := Report{
		CPU: CPUInfo{
			TempsC: []TempSensor{{Sensor: "Tctl", Value: 58.4}, {Sensor: "core0", Value: 54.0}},
		},
	}
	applyThermalOverlay(&rep, thermal.Snapshot{CPUDieC: 60.0, HaveCPU: true})
	if len(rep.CPU.TempsC) != 3 {
		t.Fatalf("TempsC len = %d, want 3", len(rep.CPU.TempsC))
	}
	if rep.CPU.TempsC[2].Sensor != "cpu_die" {
		t.Errorf("expected cpu_die appended last, got %+v", rep.CPU.TempsC)
	}
}
