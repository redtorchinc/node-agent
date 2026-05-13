// Package ollama is the platforms-package adapter over the existing
// internal/ollama client. It converts ollama.Info into the canonical
// platforms.Report/Model shape so /health.platforms.ollama agrees with the
// vLLM entry on structure.
//
// The lower-level internal/ollama package is unchanged — it still serves
// the legacy top-level /health.ollama field for v0.1.x backwards compat.
// This package is the new road.
package ollama

import (
	"context"
	"strings"
	"time"

	"github.com/redtorchinc/node-agent/internal/config"
	legacy "github.com/redtorchinc/node-agent/internal/ollama"
	"github.com/redtorchinc/node-agent/internal/platforms"
)

// Detector wraps a legacy.Client and exposes the platforms.Platform shape.
type Detector struct {
	client *legacy.Client
	cfg    config.PlatformEntry
}

// New returns a detector targeting the configured endpoint. The Enabled
// field is honored at probe time: a `false` value short-circuits to
// Up=false without making a network call.
func New(entry config.PlatformEntry) *Detector {
	return &Detector{
		client: legacy.NewClient(entry.Endpoint),
		cfg:    entry,
	}
}

// Name returns "ollama".
func (d *Detector) Name() string { return "ollama" }

// Probe converts the underlying ollama.Info into a platforms.Report. When
// the platform is configured `enabled: false` returns an empty Report
// with Up=false — the caller can still surface "configured but disabled".
func (d *Detector) Probe(ctx context.Context) platforms.Report {
	intervalS := int64(d.client.CacheTTL() / time.Second)
	if d.cfg.Enabled == "false" {
		return platforms.Report{
			Up:             false,
			Endpoint:       d.cfg.Endpoint,
			Models:         []platforms.Model{},
			ProbeIntervalS: intervalS,
		}
	}
	info := d.client.Probe(ctx)
	rep := platforms.Report{
		Up:             info.Up,
		Endpoint:       info.Endpoint,
		Models:         make([]platforms.Model, 0, len(info.Models)),
		Runners:        make([]platforms.Runner, 0, len(info.Runners)),
		LastScrapeTS:   info.LastProbe,
		ProbeIntervalS: intervalS,
		Stale:          isStale(info.LastProbe, intervalS, time.Now().Unix()),
	}
	for _, m := range info.Models {
		sz := m.SizeMB
		ctx := m.Context
		ttl := m.UntilS
		md := platforms.Model{
			Name:           m.Name,
			Platform:       "ollama",
			Loaded:         true,
			SizeMB:         &sz,
			Quantization:   parseQuantSuffix(m.Name),
			ContextWindow:  &ctx,
			ProcessorSplit: m.Processor,
			TTLs:           &ttl,
			LastScrapeTS:   info.LastProbe,
		}
		// Ollama exposes queued_requests on newer versions; map only `running`
		// out of an abundance of caution about which queue Ollama is reporting.
		// Waiting/Swapped are vLLM-only.
		if m.QueuedRequests > 0 {
			q := m.QueuedRequests
			md.Queue = &platforms.Queue{Running: &q}
		}
		rep.Models = append(rep.Models, md)
	}
	for _, r := range info.Runners {
		rep.Runners = append(rep.Runners, platforms.Runner{
			PID: r.PID, CPUPct: r.CPUPct, RSSMB: r.RSSMB,
		})
	}
	return rep
}

// isStale reports whether last is more than 3 × intervalS seconds before now.
// 3× is the standard staleness window — a single missed cache refresh is
// noise, three is a probe wedged.
func isStale(last, intervalS, now int64) bool {
	if last == 0 || intervalS == 0 {
		return false
	}
	return now-last > 3*intervalS
}

// parseQuantSuffix pulls a quantization tag out of an Ollama model name
// (e.g. "llama3.1:8b-instruct-q4_K_M" → "q4_K_M"). Returns "" when no
// recognisable suffix is present.
func parseQuantSuffix(name string) string {
	// Look at the last "-" segment after the first ":".
	if i := strings.Index(name, ":"); i >= 0 {
		tail := name[i+1:]
		segs := strings.Split(tail, "-")
		last := segs[len(segs)-1]
		// Common Ollama quant tags: q4_K_M, q5_0, fp16, q8_0
		if strings.HasPrefix(last, "q") || last == "fp16" || last == "bf16" {
			return last
		}
	}
	return ""
}
