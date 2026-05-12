// Package disk reports disk usage and basic IO for paths the dispatcher
// cares about (model storage, agent state, root). Cross-platform via
// gopsutil/disk; the agent doesn't shell out for disk info.
package disk

import (
	"strings"

	gdisk "github.com/shirou/gopsutil/v3/disk"
)

// Info is one entry under /health.disk[].
type Info struct {
	Path     string  `json:"path"`
	FSType   string  `json:"fstype,omitempty"`
	TotalGB  float64 `json:"total_gb"`
	UsedGB   float64 `json:"used_gb"`
	UsedPct  float64 `json:"used_pct"`
	IOPSRead  *uint64 `json:"iops_read,omitempty"`
	IOPSWrite *uint64 `json:"iops_write,omitempty"`
}

// Probe returns disk usage for the configured paths plus auto-discovered
// mounts with >= 50GB total. Capped at 10 entries to bound /health
// payload size.
//
// `configured` is the operator-supplied list. Empty means use platform
// defaults (/, /var/lib/ollama, /var/lib/rt-node-agent).
func Probe(configured []string) []Info {
	want := map[string]bool{}
	paths := []string{}
	add := func(p string) {
		if p == "" || want[p] {
			return
		}
		want[p] = true
		paths = append(paths, p)
	}
	for _, p := range configured {
		add(p)
	}
	if len(paths) == 0 {
		for _, p := range defaultPaths() {
			add(p)
		}
	}

	// Auto-discover large mounts (>= 50 GB total) that aren't already listed.
	partitions, err := gdisk.Partitions(false)
	if err == nil {
		for _, p := range partitions {
			if len(paths) >= 10 {
				break
			}
			if want[p.Mountpoint] {
				continue
			}
			u, err := gdisk.Usage(p.Mountpoint)
			if err != nil || u == nil {
				continue
			}
			if u.Total/(1024*1024*1024) >= 50 {
				add(p.Mountpoint)
			}
		}
	}

	out := make([]Info, 0, len(paths))
	for _, p := range paths {
		u, err := gdisk.Usage(p)
		if err != nil || u == nil {
			continue
		}
		out = append(out, Info{
			Path:    p,
			FSType:  strings.ToLower(u.Fstype),
			TotalGB: round2(float64(u.Total) / 1024 / 1024 / 1024),
			UsedGB:  round2(float64(u.Used) / 1024 / 1024 / 1024),
			UsedPct: round2(u.UsedPercent),
		})
	}
	return out
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
