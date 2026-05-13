package health

import (
	"testing"

	"github.com/redtorchinc/node-agent/internal/gpu"
	"github.com/redtorchinc/node-agent/internal/mem"
	"github.com/redtorchinc/node-agent/internal/rdma"
)

// TestApplyUnifiedMemoryDerivation_GB10WithProcesses simulates the GB10 field
// case: vLLM holding ~86 GB on a 128 GB unified-memory box. The composer
// must derive a VRAM ceiling from system memory and usage from the
// per-process accounting, otherwise vram_used_pct stays at 0 and
// load-aware dispatch can't rank the node.
func TestApplyUnifiedMemoryDerivation_GB10WithProcesses(t *testing.T) {
	rep := Report{
		Memory: mem.Info{TotalMB: 131072, UsedMB: 90000, UsedPct: 68.7},
		GPUs: []gpu.GPU{{
			Name:        "NVIDIA GB10",
			VRAMUnified: true,
			Processes: []gpu.Process{
				{PID: 2740534, Name: "vllm", VRAMUsedMB: 86892},
			},
		}},
	}
	applyUnifiedMemoryDerivation(&rep)
	if !rep.Memory.Unified {
		t.Errorf("memory.unified must flip true when any GPU is unified")
	}
	g := rep.GPUs[0]
	if g.VRAMTotalMB != 131072 {
		t.Errorf("VRAMTotalMB = %d, want 131072 (from memory.total_mb)", g.VRAMTotalMB)
	}
	if g.VRAMUsedMB != 86892 {
		t.Errorf("VRAMUsedMB = %d, want 86892 (sum of Processes[].VRAMUsedMB)", g.VRAMUsedMB)
	}
	if pct := int(g.VRAMUsedPct); pct < 65 || pct > 70 {
		t.Errorf("VRAMUsedPct = %.2f, want ~66 (86892/131072)", g.VRAMUsedPct)
	}
}

// Apple Silicon has no per-process VRAM accounting at all. The composer
// must fall back to host memory.used_mb so the ranker still sees real load.
func TestApplyUnifiedMemoryDerivation_AppleNoProcesses(t *testing.T) {
	rep := Report{
		Memory: mem.Info{TotalMB: 65536, UsedMB: 32768, UsedPct: 50, Unified: true},
		GPUs: []gpu.GPU{{
			Name:        "Apple M3 Max",
			VRAMUnified: true,
			Processes:   []gpu.Process{},
		}},
	}
	applyUnifiedMemoryDerivation(&rep)
	g := rep.GPUs[0]
	if g.VRAMTotalMB != 65536 {
		t.Errorf("VRAMTotalMB = %d, want 65536", g.VRAMTotalMB)
	}
	if g.VRAMUsedMB != 32768 {
		t.Errorf("VRAMUsedMB = %d, want 32768 (memory.used_mb fallback)", g.VRAMUsedMB)
	}
	if pct := int(g.VRAMUsedPct); pct != 50 {
		t.Errorf("VRAMUsedPct = %.2f, want 50", g.VRAMUsedPct)
	}
}

// A discrete GPU (VRAMUnified=false) must be left untouched, including when
// running alongside a unified GPU in the same report.
func TestApplyUnifiedMemoryDerivation_DiscreteUntouched(t *testing.T) {
	rep := Report{
		Memory: mem.Info{TotalMB: 131072, UsedMB: 20000},
		GPUs: []gpu.GPU{{
			Name:        "NVIDIA RTX 3090",
			VRAMUnified: false,
			VRAMTotalMB: 24576,
			VRAMUsedMB:  12288,
			VRAMUsedPct: 50,
		}},
	}
	applyUnifiedMemoryDerivation(&rep)
	if rep.Memory.Unified {
		t.Errorf("memory.unified must NOT flip when only discrete GPUs are present")
	}
	g := rep.GPUs[0]
	if g.VRAMTotalMB != 24576 || g.VRAMUsedMB != 12288 || g.VRAMUsedPct != 50 {
		t.Errorf("discrete GPU mutated: %+v", g)
	}
}

// Regression: a unified-memory NVIDIA box should be able to fire
// vram_over_95pct via the standard maxVRAMPct path — the whole point of
// the derivation is that this reason is no longer silenced on GB10.
func TestApplyUnifiedMemoryDerivation_TriggersVRAMOver95(t *testing.T) {
	rep := Report{
		Memory: mem.Info{TotalMB: 131072, UsedMB: 128000},
		GPUs: []gpu.GPU{{
			VRAMUnified: true,
			Processes:   []gpu.Process{{VRAMUsedMB: 128000}},
		}},
	}
	applyUnifiedMemoryDerivation(&rep)
	if maxVRAMPct(rep) <= 95 {
		t.Errorf("expected maxVRAMPct > 95 on saturated unified-memory box; got %.2f", maxVRAMPct(rep))
	}
}

// applyGPUDirectCapability: unified-memory GPU → GPUDirectSupported=false
// (the GB10 / DGX Spark architectural carve-out).
func TestApplyGPUDirectCapability_UnifiedSetsFalse(t *testing.T) {
	rep := Report{
		GPUs: []gpu.GPU{{Name: "NVIDIA GB10", VRAMUnified: true}},
		RDMA: &rdma.Info{Enabled: true, KernelModules: map[string]bool{"mlx5_ib": true}},
	}
	applyGPUDirectCapability(&rep)
	if rep.RDMA.GPUDirectSupported == nil {
		t.Fatalf("expected non-nil GPUDirectSupported on unified GPU")
	}
	if *rep.RDMA.GPUDirectSupported {
		t.Errorf("unified-memory GPU must report GPUDirectSupported=false")
	}
}

// Discrete NVIDIA → GPUDirectSupported=true.
func TestApplyGPUDirectCapability_DiscreteSetsTrue(t *testing.T) {
	rep := Report{
		GPUs: []gpu.GPU{{Name: "NVIDIA H100 80GB HBM3"}},
		RDMA: &rdma.Info{Enabled: true, KernelModules: map[string]bool{"mlx5_ib": true}},
	}
	applyGPUDirectCapability(&rep)
	if rep.RDMA.GPUDirectSupported == nil || !*rep.RDMA.GPUDirectSupported {
		t.Errorf("discrete NVIDIA → GPUDirectSupported=true; got %v", rep.RDMA.GPUDirectSupported)
	}
}

// No NVIDIA GPU (Apple Silicon, AMD): leave nil — consumers treat as
// "GPUDirect status unknown" rather than assuming either way.
func TestApplyGPUDirectCapability_NoNvidiaLeavesNil(t *testing.T) {
	rep := Report{
		GPUs: []gpu.GPU{{Name: "Apple M4 Max", VRAMUnified: true}},
		RDMA: &rdma.Info{Enabled: true},
	}
	applyGPUDirectCapability(&rep)
	// Apple Silicon is unified-memory → still sets false (the
	// "GPUDirect not supported" semantic holds for any unified-memory
	// platform; we'd never expect nvidia_peermem there).
	if rep.RDMA.GPUDirectSupported == nil || *rep.RDMA.GPUDirectSupported {
		t.Errorf("unified Apple Silicon → GPUDirectSupported=false (no nvidia_peermem expected); got %v", rep.RDMA.GPUDirectSupported)
	}
}

// RDMA absent: applyGPUDirectCapability is a no-op (no panic).
func TestApplyGPUDirectCapability_NoRDMABlock(t *testing.T) {
	rep := Report{
		GPUs: []gpu.GPU{{Name: "NVIDIA GB10", VRAMUnified: true}},
		RDMA: nil,
	}
	applyGPUDirectCapability(&rep) // must not panic
}
