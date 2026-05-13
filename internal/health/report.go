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
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"

	"github.com/redtorchinc/node-agent/internal/allocators"
	"github.com/redtorchinc/node-agent/internal/buildinfo"
	"github.com/redtorchinc/node-agent/internal/config"
	"github.com/redtorchinc/node-agent/internal/databases"
	"github.com/redtorchinc/node-agent/internal/gpu"
	"github.com/redtorchinc/node-agent/internal/mem"
	"github.com/redtorchinc/node-agent/internal/ollama"
	"github.com/redtorchinc/node-agent/internal/platforms"
	pollama "github.com/redtorchinc/node-agent/internal/platforms/ollama"
	"github.com/redtorchinc/node-agent/internal/platforms/vllm"
	"github.com/redtorchinc/node-agent/internal/rdma"
	"github.com/redtorchinc/node-agent/internal/sysmetrics/disk"
	"github.com/redtorchinc/node-agent/internal/sysmetrics/network"
	"github.com/redtorchinc/node-agent/internal/sysmetrics/storage"
	"github.com/redtorchinc/node-agent/internal/sysmetrics/timesync"
)

// Report is the full /health JSON payload.
type Report struct {
	Ts           int64  `json:"ts"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agent_version"`
	UptimeS      int64  `json:"uptime_s"`

	CPU    CPUInfo   `json:"cpu"`
	Memory mem.Info  `json:"memory"`
	GPUs   []gpu.GPU `json:"gpus"`

	// Disk and Network are v0.2.0 additions. Empty arrays when not measurable.
	Disk    []disk.Info       `json:"disk"`
	Network network.Info      `json:"network"`
	TimeSync *timesync.Info   `json:"time_sync,omitempty"`

	// Storage is v0.2.3: detected NAS / pooled storage (ZFS pools, NFS,
	// CIFS, Ceph, GlusterFS, Lustre). Empty array when nothing matches.
	Storage []storage.Info `json:"storage"`

	// TopSwapProcesses is v0.2.3: top N processes ranked by VmSwap. Linux
	// only (other platforms emit empty array). Capped at 10 entries.
	TopSwapProcesses []mem.SwapProcess `json:"top_swap_processes"`

	// Databases is v0.2.3: auto-detected database servers running on the
	// node (Postgres, MySQL, MongoDB, Redis, Neo4j, Chroma, …). Empty
	// array when nothing matches.
	Databases []databases.Database `json:"databases"`

	ServiceAllocs []allocators.Scraped `json:"service_allocators"`

	// Platforms is the v0.2.0 home for inference-platform state. Keyed by
	// platform name (ollama, vllm). Empty map when nothing is configured.
	Platforms map[string]platforms.Report `json:"platforms"`

	// Services lists state for allowlisted units (POST /actions/service).
	// Always emitted (possibly empty) — its presence tells the backend the
	// agent supports remote service control.
	Services []ServiceState `json:"services"`

	// Ollama is the legacy v0.1.x field. Kept as an alias of platforms.ollama
	// for the duration of v0.2.x. Removed in v0.3.0.
	Ollama ollama.Info `json:"ollama"`

	// RDMA is populated on Linux hosts with /sys/class/infiniband present.
	// Omitted from /health entirely (nil) when no IB devices exist.
	RDMA *rdma.Info `json:"rdma,omitempty"`

	// Mode and Training are Phase B; populated only when training-mode is
	// engaged. Always emitted: Mode defaults to "idle"/"inference".
	Mode     string         `json:"mode"`
	Training *TrainingState `json:"training,omitempty"`

	// Degraded is true iff any HARD reason fires. Kept for v0.1.x / v0.2.x
	// backend compatibility — equivalent to DegradedHard. Will be removed
	// in v0.3.0 once all consumers switch to the explicit booleans.
	Degraded bool `json:"degraded"`

	// DegradedHard is true iff any hard reason is present in DegradedReasons.
	// Dispatchers must skip a node where DegradedHard=true.
	DegradedHard bool `json:"degraded_hard"`

	// DegradedSoft is true iff any soft reason is present in DegradedReasons.
	// Dispatchers may deprioritize but still use a soft-degraded node.
	// DegradedHard and DegradedSoft are independent — both can be true if
	// both kinds of reasons are firing.
	DegradedSoft bool `json:"degraded_soft"`

	// DegradedReasons is the unified list (hard first in SPEC order, then
	// soft in SPEC order). Membership is the source of truth; the booleans
	// are derived.
	DegradedReasons []string `json:"degraded_reasons"`
}

// ServiceState is one allowlisted unit's state, surfaced under /health.services[].
// Populated by the services package; defined here so the JSON contract is
// in one place.
type ServiceState struct {
	Unit        string `json:"unit"`
	ActiveState string `json:"active_state,omitempty"`
	SubState    string `json:"sub_state,omitempty"`
	MainPID     int    `json:"main_pid,omitempty"`
	MemoryMB    int64  `json:"memory_mb,omitempty"`
}

// TrainingState is /health.training, present only when mode = training_mode.
// It surfaces what the agent will reload on exit (config-driven, advisory)
// and the run-id under which training was entered.
type TrainingState struct {
	RunID                 string   `json:"run_id"`
	EnteredAt             int64    `json:"entered_at"`
	ExpectedDurationS     int64    `json:"expected_duration_s,omitempty"`
	OllamaModelsReleased  []string `json:"ollama_models_released"`
	OllamaModelsToRestore []string `json:"ollama_models_to_restore"`
}

// ModeReporter exposes the mode-state-machine snapshot to the health
// reporter without an import cycle. Implementations live in internal/mode.
type ModeReporter interface {
	Mode() string
	Training() *TrainingState
}

// CPUInfo mirrors /health.cpu. v0.2.0 adds model/vendor/usage_pct/freq/temps
// as additive fields; v0.1.x clients ignore them.
type CPUInfo struct {
	Model           string  `json:"model,omitempty"`
	Vendor          string  `json:"vendor,omitempty"`
	CoresPhysical   int     `json:"cores_physical"`
	CoresLogical    int     `json:"cores_logical"`
	FreqMHzCurrent  *int    `json:"freq_mhz_current,omitempty"`
	FreqMHzMin      *int    `json:"freq_mhz_min,omitempty"`
	FreqMHzMax      *int    `json:"freq_mhz_max,omitempty"`
	UsagePct        *float64 `json:"usage_pct,omitempty"`
	UsagePerCorePct []float64 `json:"usage_per_core_pct,omitempty"`
	Load1m          float64 `json:"load_1m"`
	Load5m          float64 `json:"load_5m"`
	Load15m         float64 `json:"load_15m"`
	TempsC          []TempSensor `json:"temps_c,omitempty"`
	Throttled       *bool   `json:"throttled,omitempty"`
	ThrottleReasons []string `json:"throttle_reasons,omitempty"`
}

// TempSensor is one (sensor-name, value-in-celsius) reading. Sensor names
// are platform-specific (Tctl, core0, cpu_thermal, etc) — passed through
// verbatim so operators recognise them.
type TempSensor struct {
	Sensor string  `json:"sensor"`
	Value  float64 `json:"value"`
}

// ServicesReporter is the optional source of /health.services[].
// Implementations live in internal/services; the Reporter holds an
// interface here to avoid an import cycle.
type ServicesReporter interface {
	Snapshot(ctx context.Context) []ServiceState
}

// Reporter builds Reports on demand. Construct via NewReporter; safe for
// concurrent Report() calls.
type Reporter struct {
	Cfg        config.Config
	GPU        gpu.Probe
	Ollama     *ollama.Client
	Allocators *allocators.Store

	platforms []platforms.Platform // detectors in stable order
	services  ServicesReporter
	mode      ModeReporter

	start time.Time
	now   func() time.Time
}

// NewReporter wires a Reporter from config. Side-effects: starts per-service
// allocator scrape goroutines (tied to ctx from Run, plumbed via Start).
func NewReporter(cfg config.Config) (*Reporter, error) {
	r := &Reporter{
		Cfg:        cfg,
		GPU:        gpu.Select(),
		Ollama:     ollama.NewClient(cfg.Platforms.Ollama.Endpoint),
		Allocators: allocators.NewStore(),
		start:      time.Now(),
		now:        time.Now,
	}
	r.platforms = []platforms.Platform{
		pollama.New(cfg.Platforms.Ollama),
		vllm.New(cfg.Platforms.VLLM),
	}
	return r, nil
}

// SetServicesReporter wires the services snapshot source. Optional; when
// nil, /health.services is the empty array.
func (r *Reporter) SetServicesReporter(s ServicesReporter) { r.services = s }

// SetModeReporter wires the mode-state-machine snapshot source. Optional;
// when nil, /health.mode falls back to "idle" / "inference" derived from
// platforms[].models.
func (r *Reporter) SetModeReporter(m ModeReporter) { r.mode = m }

// StartBackground launches per-service allocator scrape loops and warms
// the GPU and database probe caches. Both warmups run in detached
// goroutines so the HTTP listener can bind immediately — previously the
// GPU warmup blocked the caller for up to 2s, which raced install.sh's
// post-install healthcheck and made fresh installs report false "did
// not respond on port 11435" failures even though the agent was just
// finishing its startup probe.
//
// Cold /health responses pay the un-cached probe cost (typically <1s on
// Linux NVIDIA, 1-2s on darwin/Apple Silicon) until each probe's first
// successful warm. The case-manager's 2s client timeout retries are
// designed for this.
func (r *Reporter) StartBackground(ctx context.Context) {
	// Seed gopsutil's internal cpu.Times snapshot so the first /health
	// call's cpu.Percent(0, …) returns a real delta rather than 0.
	// Without this seed, /health on a freshly-started agent reports
	// usage_pct: 0 (or nil on flake) until the second call.
	go func() {
		_, _ = cpu.Percent(0, false)
		_, _ = cpu.Percent(0, true)
	}()

	// Initial warmups + a periodic keep-warm ticker. The case-manager
	// caches /health for 30s, so it polls every ~30s — which is exactly
	// long enough for our 30s probe TTLs to expire between polls. The
	// ticker keeps caches hot by refreshing every 25s, so backend polls
	// always hit warm and pay only the ~10ms render cost. Stops when
	// ctx is cancelled (agent shutdown).
	go r.keepWarmGPU(ctx)
	go r.keepWarmDatabases(ctx)
	go r.keepWarmLegacyOllama(ctx)
	go r.keepWarmPlatforms(ctx)

	for _, sc := range r.Cfg.ServiceAllocators {
		s := allocators.New(sc, r.Allocators)
		// Wire only_when_mode through the mode reporter so the scrape loop
		// skips when the mode doesn't match. r.mode may still be nil when
		// the server hasn't called SetModeReporter yet; the scraper handles
		// a nil oracle as "never mode-active" for safety.
		if r.mode != nil {
			s = s.WithMode(modeOracleFunc(r.mode.Mode))
		}
		go s.Start(ctx)
	}
}

// modeOracleFunc adapts a Mode() callback to the allocators.ModeOracle
// interface so we can pass a method value without an intermediate struct.
type modeOracleFunc func() string

// Mode implements allocators.ModeOracle.
func (f modeOracleFunc) Mode() string { return f() }

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

	applyUnifiedMemoryDerivation(&rep)

	rep.ServiceAllocs = r.Allocators.Snapshot()
	if rep.ServiceAllocs == nil {
		rep.ServiceAllocs = []allocators.Scraped{}
	}

	// Platforms — gather concurrently? Keep sequential for simplicity, each
	// detector has its own 2s timeout and 5s response cache.
	rep.Platforms = map[string]platforms.Report{}
	for _, p := range r.platforms {
		pCtx, pCancel := context.WithTimeout(ctx, 2*time.Second)
		rep.Platforms[p.Name()] = p.Probe(pCtx)
		pCancel()
	}

	// Keep the legacy /health.ollama field populated so v0.1.x backends
	// continue to work unchanged. New consumers should switch to platforms.
	oCtx, oCancel := context.WithTimeout(ctx, 2*time.Second)
	rep.Ollama = r.Ollama.Probe(oCtx)
	oCancel()

	rep.Disk = disk.Probe(r.Cfg.Disk.Paths)
	if rep.Disk == nil {
		rep.Disk = []disk.Info{}
	}
	rep.Network = network.Probe()

	rep.Storage = storage.Probe()
	rep.TopSwapProcesses = mem.TopSwapProcesses(10)
	if rep.TopSwapProcesses == nil {
		rep.TopSwapProcesses = []mem.SwapProcess{}
	}
	dbCtx, dbCancel := context.WithTimeout(ctx, 2*time.Second)
	rep.Databases = databases.Probe(dbCtx)
	dbCancel()
	if rep.Databases == nil {
		rep.Databases = []databases.Database{}
	}

	if ts := timesync.Probe(ctx); ts != nil {
		rep.TimeSync = ts
	}

	rep.Services = []ServiceState{}
	if r.services != nil {
		sCtx, sCancel := context.WithTimeout(ctx, 2*time.Second)
		rep.Services = r.services.Snapshot(sCtx)
		sCancel()
		if rep.Services == nil {
			rep.Services = []ServiceState{}
		}
	}

	rep.RDMA = rdma.Probe(ctx)

	rep.Mode = deriveMode(rep, r.mode)
	if r.mode != nil {
		rep.Training = r.mode.Training()
	}

	deg, reasons := Evaluate(rep, r.Cfg, r.now())
	rep.Degraded = deg
	rep.DegradedHard = deg
	rep.DegradedSoft = hasSoftReason(reasons)
	rep.DegradedReasons = reasons
	if rep.DegradedReasons == nil {
		rep.DegradedReasons = []string{}
	}

	return rep, nil
}

// keepWarmInterval is the cadence at which background goroutines refresh
// probe caches. Sized to sit comfortably under the underlying 30s cache
// TTLs so the cached value is always populated; sized over the
// case-manager's ~30s poll period so consecutive backend calls hit a
// warm cache rather than racing the probe.
const keepWarmInterval = 25 * time.Second

// refresher is the contract that lets a keep-warm ticker force a fresh
// underlying probe, bypassing TTL. Cache implementations satisfy it.
type refresher interface {
	Refresh(ctx context.Context)
}

// keepWarmGPU runs the GPU probe immediately and then every
// keepWarmInterval. Uses CachedProbe.Refresh so each tick fires a real
// scrape — calling Probe() during the cache's fresh window would
// no-op, and the next /health after TTL expiry would still pay the
// cold cost.
func (r *Reporter) keepWarmGPU(ctx context.Context) {
	refresh := func() {
		wctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if c, ok := r.GPU.(*gpu.CachedProbe); ok {
			_ = c.Refresh(wctx)
		} else {
			_, _ = r.GPU.Probe(wctx)
		}
	}
	refresh()
	t := time.NewTicker(keepWarmInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			refresh()
		}
	}
}

// keepWarmDatabases primes the databases probe (gopsutil/net + process
// enumeration) and refreshes on the same cadence. On macOS the cold
// socket enumeration shells to lsof and can spike past the /health
// budget; this ticker keeps the cache populated so /health always
// reads a fast path.
func (r *Reporter) keepWarmDatabases(ctx context.Context) {
	refresh := func() {
		wctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		databases.Refresh(wctx)
	}
	refresh()
	t := time.NewTicker(keepWarmInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			refresh()
		}
	}
}

// keepWarmLegacyOllama refreshes the legacy /health.ollama field's cache.
// Backed by the same legacy ollama.Client used by the platforms.ollama
// adapter, so warming this also warms platforms.ollama.
func (r *Reporter) keepWarmLegacyOllama(ctx context.Context) {
	refresh := func() {
		wctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		r.Ollama.Refresh(wctx)
	}
	refresh()
	t := time.NewTicker(keepWarmInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			refresh()
		}
	}
}

// keepWarmPlatforms refreshes every configured platform detector. The
// detectors hold their own caches; this ticker calls Refresh on each
// (or falls back to Probe for detectors that don't expose one) so the
// caches stay populated for /health.
func (r *Reporter) keepWarmPlatforms(ctx context.Context) {
	refresh := func() {
		for _, p := range r.platforms {
			wctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if rf, ok := p.(refresher); ok {
				rf.Refresh(wctx)
			} else {
				_ = p.Probe(wctx)
			}
			cancel()
		}
	}
	refresh()
	t := time.NewTicker(keepWarmInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			refresh()
		}
	}
}

// applyUnifiedMemoryDerivation fills the VRAM ceiling and usage for
// unified-memory GPUs (Apple Silicon, NVIDIA GB10 Grace-Blackwell) and
// flips memory.unified=true on the report. Required because nvidia-smi on
// GB10 reports memory.total=[N/A] and Apple's system_profiler probe has
// no per-process VRAM accounting — without this, vram_used_pct stays at
// 0 and load-aware dispatch can't distinguish a saturated unified-memory
// node from a fresh idle one.
//
// Derivation rule per unified GPU:
//   - VRAMTotalMB: take from memory.total_mb when probe reports 0.
//   - VRAMUsedMB:  sum the per-process accounting when probe reports 0.
//     Falls back to memory.used_mb when no per-process data is available
//     (Apple Silicon has none; the resulting "vram_used = host RAM used"
//     is exactly the signal the ranker uses on Apple today).
//   - VRAMUsedPct: recomputed from the derived total/used.
func applyUnifiedMemoryDerivation(rep *Report) {
	hasUnified := false
	for i := range rep.GPUs {
		g := &rep.GPUs[i]
		if !g.VRAMUnified {
			continue
		}
		hasUnified = true
		if g.VRAMTotalMB == 0 {
			g.VRAMTotalMB = rep.Memory.TotalMB
		}
		if g.VRAMUsedMB == 0 {
			var sum int64
			for _, p := range g.Processes {
				sum += p.VRAMUsedMB
			}
			if sum > 0 {
				g.VRAMUsedMB = sum
			} else {
				// No per-process accounting (Apple Silicon, or older nvidia-smi
				// on GB10 without compute-apps support). Fall back to host RAM
				// usage — on a unified-memory box that IS the GPU footprint.
				g.VRAMUsedMB = rep.Memory.UsedMB
			}
		}
		if g.VRAMTotalMB > 0 {
			g.VRAMUsedPct = float64(g.VRAMUsedMB) / float64(g.VRAMTotalMB) * 100
		}
	}
	if hasUnified {
		rep.Memory.Unified = true
	}
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
	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		c.Model = infos[0].ModelName
		c.Vendor = infos[0].VendorID
		if mhz := infos[0].Mhz; mhz > 0 {
			cur := int(mhz)
			c.FreqMHzCurrent = &cur
		}
	}
	// usage_pct: gopsutil's cpu.Percent(0, …) returns delta-since-last-call
	// — instant, no sleep. StartBackground seeds the internal "last times"
	// state at agent start so the first /health call gets a real value
	// rather than 0 or nil. The previous 100ms sample interval was
	// 100ms × every-/health-call wasted on darwin where /health is hot.
	if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
		v := round2(pct[0])
		c.UsagePct = &v
	}
	if per, err := cpu.Percent(0, true); err == nil && len(per) > 0 {
		c.UsagePerCorePct = make([]float64, len(per))
		for i, v := range per {
			c.UsagePerCorePct[i] = round2(v)
		}
	}
	// Apple Silicon: gopsutil leaves cpu.Info.VendorID empty (Apple
	// doesn't populate the legacy x86 VendorID register). Hardcode the
	// canonical value so consumers don't have to special-case nil.
	// Same logic for Asahi Linux on Apple hardware (model contains "Apple").
	if c.Vendor == "" && (runtime.GOOS == "darwin" || strings.Contains(c.Model, "Apple")) {
		c.Vendor = "Apple"
	}
	if temps, err := host.SensorsTemperatures(); err == nil {
		for _, t := range temps {
			if t.Temperature <= 0 {
				continue
			}
			c.TempsC = append(c.TempsC, TempSensor{
				Sensor: t.SensorKey,
				Value:  round2(t.Temperature),
			})
		}
	}
	return c
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

// deriveMode resolves the wire value for /health.mode. When a ModeReporter
// is wired (Phase B), its answer wins (it carries the explicit
// "training_mode" state). Without one, the agent derives:
//
//	- "inference" if any platform reports a loaded model
//	- "idle"      otherwise
func deriveMode(rep Report, m ModeReporter) string {
	if m != nil {
		if v := m.Mode(); v != "" {
			return v
		}
	}
	for _, p := range rep.Platforms {
		if len(p.Models) > 0 {
			return "inference"
		}
	}
	if len(rep.Ollama.Models) > 0 {
		return "inference"
	}
	return "idle"
}

// ErrNotReady is returned when Report() is called before the background
// loops have reported anything. Currently unused (allocators start with an
// immediate scrape), but reserved in case future probes become async.
var ErrNotReady = errors.New("reporter not ready")

// String is convenient for log lines.
func (r Report) String() string {
	return fmt.Sprintf("host=%s os=%s/%s gpus=%d ollama=%v platforms=%d degraded=%v reasons=%v",
		r.Hostname, r.OS, r.Arch, len(r.GPUs), r.Ollama.Up, len(r.Platforms), r.Degraded, r.DegradedReasons)
}
