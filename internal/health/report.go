// Package health composes the /health response and evaluates degraded_reasons.
//
// The Report struct is a cross-repo contract with the case-manager backend
// (see SPEC.md §HTTP API). Renaming or removing fields is a breaking change;
// additive changes are safe because the backend only reads fields it knows.
package health

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"

	"github.com/redtorchinc/node-agent/internal/allocators"
	"github.com/redtorchinc/node-agent/internal/buildinfo"
	"github.com/redtorchinc/node-agent/internal/config"
	"github.com/redtorchinc/node-agent/internal/gpu"
	"github.com/redtorchinc/node-agent/internal/mem"
	"github.com/redtorchinc/node-agent/internal/ollama"
)

// Report is the full /health JSON payload.
type Report struct {
	Ts              int64                 `json:"ts"`
	Hostname        string                `json:"hostname"`
	OS              string                `json:"os"`
	Arch            string                `json:"arch"`
	AgentVersion    string                `json:"agent_version"`
	UptimeS         int64                 `json:"uptime_s"`
	CPU             CPUInfo               `json:"cpu"`
	Memory          mem.Info              `json:"memory"`
	GPUs            []gpu.GPU             `json:"gpus"`
	ServiceAllocs   []allocators.Scraped  `json:"service_allocators"`
	Ollama          ollama.Info           `json:"ollama"`
	Degraded        bool                  `json:"degraded"`
	DegradedReasons []string              `json:"degraded_reasons"`
}

// CPUInfo mirrors /health.cpu.
type CPUInfo struct {
	CoresPhysical int     `json:"cores_physical"`
	CoresLogical  int     `json:"cores_logical"`
	Load1m        float64 `json:"load_1m"`
	Load5m        float64 `json:"load_5m"`
	Load15m       float64 `json:"load_15m"`
}

// Reporter builds Reports on demand. Construct via NewReporter; safe for
// concurrent Report() calls.
type Reporter struct {
	Cfg         config.Config
	GPU         gpu.Probe
	Ollama      *ollama.Client
	Allocators  *allocators.Store

	start time.Time
	now   func() time.Time
}

// NewReporter wires a Reporter from config. Side-effects: starts per-service
// allocator scrape goroutines (tied to ctx from Run, plumbed via Start).
func NewReporter(cfg config.Config) (*Reporter, error) {
	return &Reporter{
		Cfg:        cfg,
		GPU:        gpu.Select(),
		Ollama:     ollama.NewClient(cfg.OllamaEndpoint),
		Allocators: allocators.NewStore(),
		start:      time.Now(),
		now:        time.Now,
	}, nil
}

// StartBackground launches per-service allocator scrape loops. Returns
// immediately; goroutines exit when ctx is cancelled.
func (r *Reporter) StartBackground(ctx context.Context) {
	for _, sc := range r.Cfg.ServiceAllocators {
		s := allocators.New(sc, r.Allocators)
		go s.Start(ctx)
	}
}

// Report builds a fresh Report. It applies its own inner timeout to each
// probe so one slow probe (e.g. nvidia-smi hung) cannot stall /health
// past the outer deadline.
func (r *Reporter) Report(ctx context.Context) (Report, error) {
	rep := Report{
		Ts:           r.now().Unix(),
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		AgentVersion: buildinfo.Version,
	}
	if h, err := os.Hostname(); err == nil {
		rep.Hostname = h
	}
	if up, err := host.Uptime(); err == nil {
		rep.UptimeS = int64(up)
	} else {
		// Fall back to agent uptime if host uptime fails.
		rep.UptimeS = int64(r.now().Sub(r.start).Seconds())
	}

	rep.CPU = probeCPU()
	if mi, err := mem.Probe(ctx); err == nil {
		rep.Memory = mi
	}

	gCtx, gCancel := context.WithTimeout(ctx, 2*time.Second)
	gpus, _ := r.GPU.Probe(gCtx)
	gCancel()
	if gpus == nil {
		gpus = []gpu.GPU{}
	}
	rep.GPUs = gpus

	rep.ServiceAllocs = r.Allocators.Snapshot()
	if rep.ServiceAllocs == nil {
		rep.ServiceAllocs = []allocators.Scraped{}
	}

	oCtx, oCancel := context.WithTimeout(ctx, 2*time.Second)
	rep.Ollama = r.Ollama.Probe(oCtx)
	oCancel()

	deg, reasons := Evaluate(rep, r.Cfg, r.now())
	rep.Degraded = deg
	rep.DegradedReasons = reasons
	if rep.DegradedReasons == nil {
		rep.DegradedReasons = []string{}
	}

	return rep, nil
}

func probeCPU() CPUInfo {
	c := CPUInfo{}
	if n, err := cpu.Counts(false); err == nil {
		c.CoresPhysical = n
	}
	if n, err := cpu.Counts(true); err == nil {
		c.CoresLogical = n
	}
	if l, err := load.Avg(); err == nil && l != nil {
		c.Load1m = round2(l.Load1)
		c.Load5m = round2(l.Load5)
		c.Load15m = round2(l.Load15)
	}
	return c
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

// ErrNotReady is returned when Report() is called before the background
// loops have reported anything. Currently unused (allocators start with an
// immediate scrape), but reserved in case future probes become async.
var ErrNotReady = errors.New("reporter not ready")

// String is convenient for log lines.
func (r Report) String() string {
	return fmt.Sprintf("host=%s os=%s/%s gpus=%d ollama=%v degraded=%v reasons=%v",
		r.Hostname, r.OS, r.Arch, len(r.GPUs), r.Ollama.Up, r.Degraded, r.DegradedReasons)
}
