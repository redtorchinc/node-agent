package gpu

import "testing"

// The CSV produced by `nvidia-smi --query-gpu=...` for v0.2.0 has 25
// columns. Fixtures here pass the full set so a row-length regression is
// caught by the test, not by a silent zero-value in production.
//
// Column order MUST match queryFields in nvidia_smi.go.

const fullRow0 = "0, GPU-abc12345, NVIDIA GeForce RTX 3090, 550.54.15, 00000000:1A:00.0, " +
	"8.6, " +
	"24576, 17441, " +
	"4, 6, " +
	"43, 48, " +
	"117.25, 420.00, " +
	"1980, 2619, 1980, 1980, " +
	"0x0, " +
	"0, 0, " +
	"30, Enabled, Default, Disabled\n"

func TestParseNvidiaSMI_Basic(t *testing.T) {
	procCSV := []byte("GPU-abc12345, 556534, python3, 16188\n")

	gpus, err := ParseNvidiaSMI([]byte(fullRow0), procCSV)
	if err != nil {
		t.Fatalf("ParseNvidiaSMI: %v", err)
	}
	if len(gpus) != 1 {
		t.Fatalf("want 1 gpu, got %d", len(gpus))
	}
	g := gpus[0]
	if g.Index != 0 {
		t.Errorf("Index = %d, want 0", g.Index)
	}
	if g.Name != "NVIDIA GeForce RTX 3090" {
		t.Errorf("Name = %q", g.Name)
	}
	if g.UUID != "GPU-abc12345" {
		t.Errorf("UUID = %q", g.UUID)
	}
	if g.DriverVersion != "550.54.15" {
		t.Errorf("DriverVersion = %q", g.DriverVersion)
	}
	if g.ComputeCapability != "8.6" {
		t.Errorf("ComputeCapability = %q", g.ComputeCapability)
	}
	if g.VRAMTotalMB != 24576 || g.VRAMUsedMB != 17441 {
		t.Errorf("VRAM total/used = %d/%d", g.VRAMTotalMB, g.VRAMUsedMB)
	}
	if g.UtilPct != 4 || g.TempC != 43 {
		t.Errorf("Util/Temp = %d/%d", g.UtilPct, g.TempC)
	}
	if g.PowerW != 117 || g.PowerCapW != 420 {
		t.Errorf("Power = %d/%d", g.PowerW, g.PowerCapW)
	}
	if g.ClockGraphicsMHz != 1980 || g.ClockMemoryMHz != 2619 {
		t.Errorf("Clocks gr/mem = %d/%d", g.ClockGraphicsMHz, g.ClockMemoryMHz)
	}
	if got := int(g.VRAMUsedPct); got != 70 && got != 71 {
		t.Errorf("VRAMUsedPct = %.2f, want ~71", g.VRAMUsedPct)
	}
	if g.PersistenceMode != "Enabled" || g.MIGMode != "Disabled" {
		t.Errorf("persistence/mig = %q / %q", g.PersistenceMode, g.MIGMode)
	}
	if g.FanPct == nil || *g.FanPct != 30 {
		t.Errorf("FanPct = %v", g.FanPct)
	}
	if g.ECCVolatileUncorrected == nil || *g.ECCVolatileUncorrected != 0 {
		t.Errorf("ECC volatile = %v", g.ECCVolatileUncorrected)
	}
	if len(g.Processes) != 1 || g.Processes[0].PID != 556534 {
		t.Errorf("processes = %+v", g.Processes)
	}
}

func TestParseNvidiaSMI_MultiGPU(t *testing.T) {
	a := "0, GPU-aaa, NVIDIA A100-SXM4-80GB, 535.0, 00000000:01:00.0, 8.0, 81920, 40000, 50, 55, 55, 60, 200.0, 400.0, 1400, 1593, 1400, 1410, 0x0, 0, 0, [N/A], Enabled, Default, Disabled\n"
	b := "1, GPU-bbb, NVIDIA A100-SXM4-80GB, 535.0, 00000000:02:00.0, 8.0, 81920, 75000, 90, 92, 70, 78, 380.0, 400.0, 1410, 1593, 1410, 1410, 0x40, 0, 0, [N/A], Enabled, Default, Disabled\n"
	gpus, err := ParseNvidiaSMI([]byte(a+b), nil)
	if err != nil {
		t.Fatalf("ParseNvidiaSMI: %v", err)
	}
	if len(gpus) != 2 {
		t.Fatalf("want 2 gpus, got %d", len(gpus))
	}
	if gpus[1].VRAMUsedMB != 75000 || gpus[1].UtilPct != 90 {
		t.Errorf("gpu 1 fields wrong: %+v", gpus[1])
	}
	// GPU 1 has throttle bit 0x40 set → HW_THERMAL_SLOWDOWN
	if len(gpus[1].ThrottleReasons) != 1 || gpus[1].ThrottleReasons[0] != "HW_THERMAL_SLOWDOWN" {
		t.Errorf("throttle reasons = %v", gpus[1].ThrottleReasons)
	}
}

// TestParseNvidiaSMI_GraceHopper guards the DGX Grace Hopper arm64 case.
// SPEC §"Open questions" calls out that the CSV output format must be
// verified identical on ARM. Output format is driver-level, not
// architecture-level, so this test mirrors a captured sample.
func TestParseNvidiaSMI_GraceHopper(t *testing.T) {
	row := "0, GPU-gh200-abcd, NVIDIA GH200 480GB, 550.54.15, 00000000:01:00.0, 9.0, 97871, 1024, 0, 0, 30, 35, 90.5, 900.0, 1980, 2619, 1980, 1980, 0x0, 0, 0, [N/A], Enabled, Default, Disabled\n"
	gpus, err := ParseNvidiaSMI([]byte(row), nil)
	if err != nil {
		t.Fatalf("ParseNvidiaSMI: %v", err)
	}
	if len(gpus) != 1 || gpus[0].VRAMTotalMB != 97871 {
		t.Fatalf("bad parse: %+v", gpus)
	}
	if gpus[0].ComputeCapability != "9.0" {
		t.Errorf("Hopper compute_cap = %q, want 9.0", gpus[0].ComputeCapability)
	}
}

func TestParseNvidiaSMI_NA(t *testing.T) {
	// Older drivers emit [N/A] for some fields (e.g. power on CPU-only hosts).
	row := "0, GPU-t4, NVIDIA T4, 470.0, 00000000:01:00.0, 7.5, 15360, 100, [N/A], [N/A], 35, [N/A], [N/A], [N/A], [N/A], [N/A], [N/A], [N/A], 0x0, [N/A], [N/A], [N/A], Disabled, Default, [N/A]\n"
	gpus, err := ParseNvidiaSMI([]byte(row), nil)
	if err != nil {
		t.Fatalf("ParseNvidiaSMI: %v", err)
	}
	if gpus[0].PowerW != 0 || gpus[0].PowerCapW != 0 || gpus[0].UtilPct != 0 {
		t.Errorf("N/A fields should zero out: %+v", gpus[0])
	}
	if gpus[0].ECCVolatileUncorrected != nil {
		t.Errorf("ECC should be nil when [N/A]; got %v", gpus[0].ECCVolatileUncorrected)
	}
	if gpus[0].FanPct != nil {
		t.Errorf("FanPct should be nil when [N/A]; got %v", gpus[0].FanPct)
	}
}

// TestParseNvidiaSMI_GB10Unified guards the GB10 / Grace-Blackwell case where
// memory.total is reported as [N/A] because the GPU has no discrete VRAM
// pool. The parser must flag the GPU as unified so the health composer can
// back-fill VRAMTotalMB from system memory; without that flag,
// vram_used_pct stays at 0 and load-aware dispatch is impossible.
func TestParseNvidiaSMI_GB10Unified(t *testing.T) {
	// memory.total = [N/A], memory.used = [N/A] — the GB10 shape.
	row := "0, GPU-gb10-abcd, NVIDIA GB10, 565.0, 00000000:01:00.0, 10.0, [N/A], [N/A], 45, 12, 38, 40, 75.0, 250.0, 1500, 1500, 1500, 1500, 0x0, 0, 0, [N/A], Enabled, Default, Disabled\n"
	gpus, err := ParseNvidiaSMI([]byte(row), nil)
	if err != nil {
		t.Fatalf("ParseNvidiaSMI: %v", err)
	}
	if len(gpus) != 1 {
		t.Fatalf("want 1 gpu, got %d", len(gpus))
	}
	if !gpus[0].VRAMUnified {
		t.Errorf("GB10 GPU must have VRAMUnified=true when memory.total is [N/A]")
	}
	// Parser leaves VRAMTotalMB=0 — the health composer fills it from memory.
	if gpus[0].VRAMTotalMB != 0 {
		t.Errorf("VRAMTotalMB should stay 0 for the composer to fill; got %d", gpus[0].VRAMTotalMB)
	}
}

// A normal discrete-GPU row (memory.total present) must NOT be flagged unified.
func TestParseNvidiaSMI_DiscreteNotUnified(t *testing.T) {
	gpus, err := ParseNvidiaSMI([]byte(fullRow0), nil)
	if err != nil {
		t.Fatalf("ParseNvidiaSMI: %v", err)
	}
	if gpus[0].VRAMUnified {
		t.Errorf("discrete GPU must not be flagged VRAMUnified=true")
	}
}

// TestParseThrottleHex confirms the bitmask decomposition matches NVML.
func TestParseThrottleHex(t *testing.T) {
	got := parseThrottleHex("0x60") // SW_THERMAL (0x20) + HW_THERMAL (0x40)
	want := map[string]bool{"SW_THERMAL_SLOWDOWN": true, "HW_THERMAL_SLOWDOWN": true}
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	for _, r := range got {
		if !want[r] {
			t.Errorf("unexpected throttle reason %q", r)
		}
	}
	if r := parseThrottleHex("0x0"); r != nil {
		t.Errorf("0x0 should give nil, got %v", r)
	}
}

// TestApplyNVLink reads a small fixture matching `nvidia-smi nvlink --status`.
func TestApplyNVLink(t *testing.T) {
	gpus := []GPU{{Index: 0}, {Index: 1}}
	raw := []byte(
		"GPU 0: NVIDIA H100 80GB HBM3 (UUID: GPU-aaa)\n" +
			"         Link 0: 25 GB/s\n" +
			"         Link 1: <inactive>\n" +
			"GPU 1: NVIDIA H100 80GB HBM3 (UUID: GPU-bbb)\n" +
			"         Link 0: 25 GB/s\n",
	)
	applyNVLink(gpus, raw)
	if gpus[0].NVLink == nil || len(gpus[0].NVLink.Links) != 2 {
		t.Fatalf("gpu 0 nvlink = %+v", gpus[0].NVLink)
	}
	if gpus[0].NVLink.Links[0].State != "Up" || gpus[0].NVLink.Links[0].SpeedGBPerS != 25 {
		t.Errorf("gpu 0 link 0 = %+v", gpus[0].NVLink.Links[0])
	}
	if gpus[0].NVLink.Links[1].State != "Down" {
		t.Errorf("gpu 0 link 1 should be Down, got %s", gpus[0].NVLink.Links[1].State)
	}
	if gpus[1].NVLink == nil || len(gpus[1].NVLink.Links) != 1 {
		t.Errorf("gpu 1 nvlink = %+v", gpus[1].NVLink)
	}
}
