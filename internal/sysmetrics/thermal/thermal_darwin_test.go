//go:build darwin

package thermal

import "testing"

// sampleM2UltraOutput is real output from `powermetrics --samplers smc
// -i 1000 -n 1` on macOS 14 / M2 Ultra. Format has been stable across
// macOS 13–15 on Apple Silicon. Lines we don't care about are kept
// intact to verify the parser ignores them.
const sampleM2UltraOutput = `Machine model: Mac14,12
OS version: 23F79
Boot arguments:
Boot time: Tue Apr 22 09:31:14 2026

*** Sampled system activity (Wed Apr 23 14:02:11 2026 -0700) (1000.36ms elapsed) ***

**** SMC sensors ****

CPU die temperature: 49.66 C
GPU die temperature: 42.42 C
Fan 0 speed: 1198 rpm
Fan 1 speed: 1202 rpm
`

func TestParsePowermetricsSMC_AppleSilicon(t *testing.T) {
	cpuC, haveCPU, gpuC, haveGPU := parsePowermetricsSMC([]byte(sampleM2UltraOutput))
	if !haveCPU {
		t.Fatalf("haveCPU = false, want true")
	}
	if cpuC != 49.66 {
		t.Errorf("cpuC = %v, want 49.66", cpuC)
	}
	if !haveGPU {
		t.Fatalf("haveGPU = false, want true")
	}
	if gpuC != 42.42 {
		t.Errorf("gpuC = %v, want 42.42", gpuC)
	}
}

func TestParsePowermetricsSMC_Empty(t *testing.T) {
	_, haveCPU, _, haveGPU := parsePowermetricsSMC(nil)
	if haveCPU || haveGPU {
		t.Errorf("empty input: haveCPU=%v haveGPU=%v, want both false", haveCPU, haveGPU)
	}
}

func TestParsePowermetricsSMC_NoRootStderr(t *testing.T) {
	// Real stderr line printed when powermetrics is run as non-root.
	// We pipe stderr into stdout in some cases; verify the parser
	// doesn't latch onto unrelated text containing the prefix.
	out := []byte("powermetrics must be invoked as the superuser\n")
	_, haveCPU, _, haveGPU := parsePowermetricsSMC(out)
	if haveCPU || haveGPU {
		t.Errorf("non-root stderr: haveCPU=%v haveGPU=%v, want both false", haveCPU, haveGPU)
	}
}

func TestParsePowermetricsSMC_GPUOnly(t *testing.T) {
	// Some older M1 builds didn't surface "CPU die temperature" — only
	// the GPU line. Verify partial reads are honored.
	out := []byte("**** SMC sensors ****\n\nGPU die temperature: 38.25 C\n")
	cpuC, haveCPU, gpuC, haveGPU := parsePowermetricsSMC(out)
	if haveCPU {
		t.Errorf("haveCPU = true, want false (no CPU line in input)")
	}
	if cpuC != 0 {
		t.Errorf("cpuC = %v, want 0", cpuC)
	}
	if !haveGPU || gpuC != 38.25 {
		t.Errorf("gpu = (%v, %v), want (38.25, true)", gpuC, haveGPU)
	}
}

func TestExtractCelsius_Malformed(t *testing.T) {
	// Parser must reject lines where the numeric part won't parse —
	// avoids silently latching on to garbage if Apple ever changes the
	// suffix from " C" to something else.
	cases := []string{
		"CPU die temperature:",
		"CPU die temperature: --",
		"CPU die temperature: warm C",
	}
	for _, c := range cases {
		if v, ok := extractCelsius(c, "CPU die temperature:"); ok {
			t.Errorf("extractCelsius(%q) = (%v, true), want (_, false)", c, v)
		}
	}
}
