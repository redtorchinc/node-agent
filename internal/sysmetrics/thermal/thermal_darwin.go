//go:build darwin

package thermal

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// start launches the background refresh loop. The first refresh runs
// inline so /health calls within the first 15s of agent boot still see
// temps (otherwise the cold window would emit no temps_c / temp_c).
func (p *Probe) start(ctx context.Context) {
	p.refresh(ctx)
	go func() {
		t := time.NewTicker(refreshInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.refresh(ctx)
			}
		}
	}()
}

// refresh runs powermetrics once and parses CPU/GPU die temps.
//
// powermetrics is Apple's own thermal/power introspection tool. With
// `--samplers smc -i 1000 -n 1` it takes one SMC sample over a 1000ms
// window and exits — so the call blocks for ~1s. The outer 3s deadline
// is a safety net: if powermetrics ever wedges (it has shipped with
// crashy builds in the past), we don't pin the goroutine forever.
//
// Failure modes that all silently leave the snapshot stale:
//   - not root: powermetrics prints "must be invoked as the superuser"
//     to stderr and exits non-zero
//   - missing binary (older macOS recovery / unusual images)
//   - output format drift across macOS versions: parsePowermetricsSMC
//     just yields HaveCPU=HaveGPU=false on unrecognized labels
//
// Silent-skip is the right behavior — SPEC.md prohibits fabricating a
// missing metric, and degraded.go pointedly never fires a thermal
// reason from absent data.
func (p *Probe) refresh(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/powermetrics",
		"--samplers", "smc",
		"-i", "1000",
		"-n", "1",
	)
	out, err := cmd.Output()
	if err != nil {
		return
	}
	cpuC, haveCPU, gpuC, haveGPU := parsePowermetricsSMC(out)
	if !haveCPU && !haveGPU {
		return
	}
	p.store(Snapshot{
		CPUDieC: cpuC,
		GPUDieC: gpuC,
		HaveCPU: haveCPU,
		HaveGPU: haveGPU,
		LastTS:  time.Now(),
	})
}

// parsePowermetricsSMC scans the SMC sampler output for the two labels
// we care about. Format (verified across macOS 13–15 on M1/M2/M3):
//
//	**** SMC sensors ****
//
//	CPU die temperature: 49.66 C
//	GPU die temperature: 42.42 C
//	Fan 0 speed: 1198 rpm
//
// We only consume CPU/GPU die temps today; fan / other sensors are
// ignored. Unknown lines never error.
func parsePowermetricsSMC(out []byte) (cpuC float64, haveCPU bool, gpuC float64, haveGPU bool) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if v, ok := extractCelsius(line, "CPU die temperature:"); ok {
			cpuC = v
			haveCPU = true
			continue
		}
		if v, ok := extractCelsius(line, "GPU die temperature:"); ok {
			gpuC = v
			haveGPU = true
			continue
		}
	}
	return
}

// extractCelsius parses a "<prefix> NN.NN C" line and returns the
// numeric value. Returns (0,false) if the prefix is absent or the
// numeric part doesn't parse.
func extractCelsius(line, prefix string) (float64, bool) {
	i := strings.Index(line, prefix)
	if i < 0 {
		return 0, false
	}
	rest := strings.TrimSpace(line[i+len(prefix):])
	rest = strings.TrimSuffix(rest, " C")
	rest = strings.TrimSpace(rest)
	v, err := strconv.ParseFloat(rest, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
