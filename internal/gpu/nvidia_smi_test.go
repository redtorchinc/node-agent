package gpu

import "testing"

func TestParseNvidiaSMI_Basic(t *testing.T) {
	// Two-column separator is ", " in default nvidia-smi CSV output.
	gpuCSV := []byte("0, NVIDIA GeForce RTX 3090, 24576, 17441, 4, 43, 117.25, 420.00\n")
	procCSV := []byte("GPU-abcdef, 556534, python3, 16188\n")

	gpus, err := ParseNvidiaSMI(gpuCSV, procCSV)
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
	if g.VRAMTotalMB != 24576 || g.VRAMUsedMB != 17441 {
		t.Errorf("VRAM total/used = %d/%d", g.VRAMTotalMB, g.VRAMUsedMB)
	}
	if g.UtilPct != 4 || g.TempC != 43 {
		t.Errorf("Util/Temp = %d/%d", g.UtilPct, g.TempC)
	}
	if g.PowerW != 117 || g.PowerCapW != 420 {
		t.Errorf("Power = %d/%d", g.PowerW, g.PowerCapW)
	}
	if got := int(g.VRAMUsedPct); got != 70 && got != 71 {
		t.Errorf("VRAMUsedPct = %.2f, want ~71", g.VRAMUsedPct)
	}
	if len(g.Processes) != 1 || g.Processes[0].PID != 556534 {
		t.Errorf("processes = %+v", g.Processes)
	}
}

func TestParseNvidiaSMI_MultiGPU(t *testing.T) {
	gpuCSV := []byte(
		"0, NVIDIA A100-SXM4-80GB, 81920, 40000, 50, 55, 200.0, 400.0\n" +
			"1, NVIDIA A100-SXM4-80GB, 81920, 75000, 90, 70, 380.0, 400.0\n",
	)
	gpus, err := ParseNvidiaSMI(gpuCSV, nil)
	if err != nil {
		t.Fatalf("ParseNvidiaSMI: %v", err)
	}
	if len(gpus) != 2 {
		t.Fatalf("want 2 gpus, got %d", len(gpus))
	}
	if gpus[1].VRAMUsedMB != 75000 || gpus[1].UtilPct != 90 {
		t.Errorf("gpu 1 fields wrong: %+v", gpus[1])
	}
}

// TestParseNvidiaSMI_GraceHopper guards the DGX Grace Hopper arm64 case.
// SPEC §"Open questions" calls out that the CSV output format must be
// verified identical on ARM. Output format is driver-level, not
// architecture-level, so this test mirrors a captured sample.
func TestParseNvidiaSMI_GraceHopper(t *testing.T) {
	gpuCSV := []byte("0, NVIDIA GH200 480GB, 97871, 1024, 0, 30, 90.5, 900.0\n")
	gpus, err := ParseNvidiaSMI(gpuCSV, nil)
	if err != nil {
		t.Fatalf("ParseNvidiaSMI: %v", err)
	}
	if len(gpus) != 1 || gpus[0].VRAMTotalMB != 97871 {
		t.Fatalf("bad parse: %+v", gpus)
	}
}

func TestParseNvidiaSMI_NA(t *testing.T) {
	// Older drivers emit [N/A] for some fields (e.g. power on CPU-only hosts).
	gpuCSV := []byte("0, NVIDIA T4, 15360, 100, [N/A], 35, [N/A], [N/A]\n")
	gpus, err := ParseNvidiaSMI(gpuCSV, nil)
	if err != nil {
		t.Fatalf("ParseNvidiaSMI: %v", err)
	}
	if gpus[0].PowerW != 0 || gpus[0].PowerCapW != 0 || gpus[0].UtilPct != 0 {
		t.Errorf("N/A fields should zero out: %+v", gpus[0])
	}
}
