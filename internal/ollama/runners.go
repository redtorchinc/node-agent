package ollama

import (
	"strings"

	"github.com/shirou/gopsutil/v3/process"
)

// probeRunners enumerates `ollama runner` subprocesses (the per-model worker
// processes) and reports CPU% + RSS for each. Used by /health.ollama.runners
// and by degraded_reasons evaluation (`ollama_runner_stuck`).
func probeRunners() []Runner {
	procs, err := process.Processes()
	if err != nil {
		return []Runner{}
	}
	out := []Runner{}
	for _, p := range procs {
		name, _ := p.Name()
		if !strings.Contains(strings.ToLower(name), "ollama") {
			continue
		}
		cmd, _ := p.Cmdline()
		if !strings.Contains(cmd, "runner") {
			continue
		}
		cpu, _ := p.CPUPercent()
		rss := int64(0)
		if mi, err := p.MemoryInfo(); err == nil && mi != nil {
			rss = int64(mi.RSS / 1024 / 1024)
		}
		out = append(out, Runner{
			PID:    int(p.Pid),
			CPUPct: round2(cpu),
			RSSMB:  rss,
		})
	}
	return out
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
