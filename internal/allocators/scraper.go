// Package allocators scrapes /debug/gpu-style JSON endpoints exposed by
// cooperating Python services to surface PyTorch allocator stats.
//
// Background motivation: on 2026-04-22 the gliner2-service box held 16 GB
// of VRAM in the PyTorch cache while real usage was 2 GB. nvidia-smi shows
// only the cache total, so hardware metrics alone can't tell a "hot cache"
// node apart from a "genuinely out of memory" node. Scraping the service's
// allocator stats catches this class of leak. See SPEC.md §"Service
// allocator scraping" for the contract.
package allocators

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ServiceConfig is the per-service entry in config.yaml.
type ServiceConfig struct {
	Name              string `yaml:"name"`
	URL               string `yaml:"url"`
	ThresholdWarnMB   int64  `yaml:"threshold_warn_mb"`
	ThresholdCritMB   int64  `yaml:"threshold_critical_mb"`
	ScrapeIntervalS   int    `yaml:"scrape_interval_s"`

	// OnlyWhenMode, if set, restricts scraping to ticks where the agent's
	// mode matches. The typical use is "only_when_mode: training_mode" on
	// the training-process allocator so its socket isn't poked while idle
	// (and we don't fill the log with connection-refused noise).
	OnlyWhenMode string `yaml:"only_when_mode"`
}

// Scraped is one allocator entry as it appears in /health.service_allocators[].
type Scraped struct {
	Name             string  `json:"name"`
	URL              string  `json:"url"`
	ScrapeOK         bool    `json:"scrape_ok"`
	AllocatedMB      float64 `json:"allocated_mb"`
	ReservedMB       float64 `json:"reserved_mb"`
	MaxAllocatedMB   float64 `json:"max_allocated_mb"`
	CacheOverheadPct float64 `json:"cache_overhead_pct"`
	LastScrapeTS     int64   `json:"last_scrape_ts"`

	// Extra holds any fields the service emits beyond the canonical three.
	// Training services use this to surface run_id, step, epoch, loss,
	// tokens_per_second — opaque to the agent, pass-through to the
	// dispatcher. JSON RawMessage preserves the exact shape per service.
	Extra map[string]json.RawMessage `json:"extra,omitempty"`

	// ThresholdWarnMB/CritMB are copied from config so the /health evaluator
	// can decide vram_service_creep_* without re-reading the config struct.
	ThresholdWarnMB int64 `json:"-"`
	ThresholdCritMB int64 `json:"-"`
}

// response is the expected shape emitted by a cooperating service.
// canonicalFields are the three required keys; everything else is stashed
// in Scraped.Extra so the dispatcher can read training-specific fields
// (run_id, step, epoch, loss_train, tokens_per_second, …) verbatim.
type response struct {
	AllocatedMB    float64 `json:"allocated_mb"`
	ReservedMB     float64 `json:"reserved_mb"`
	MaxAllocatedMB float64 `json:"max_allocated_mb"`
}

// canonicalFields enumerates the fields owned by the agent. parseExtra
// copies anything else into Scraped.Extra.
var canonicalFields = map[string]struct{}{
	"allocated_mb":      {},
	"reserved_mb":       {},
	"max_allocated_mb":  {},
}

// Store holds the most recent scrape result per service. Safe for concurrent
// use; the /health handler reads Snapshot() while the scrape loop writes.
type Store struct {
	mu    sync.RWMutex
	items map[string]Scraped
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{items: map[string]Scraped{}} }

// Put upserts an entry by name.
func (s *Store) Put(e Scraped) {
	s.mu.Lock()
	s.items[e.Name] = e
	s.mu.Unlock()
}

// Snapshot returns a copy of all entries in no guaranteed order.
func (s *Store) Snapshot() []Scraped {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Scraped, 0, len(s.items))
	for _, v := range s.items {
		out = append(out, v)
	}
	return out
}

// ModeOracle is the minimal interface the scraper needs to honour
// only_when_mode. The mode package's Manager.InTraining + "training_mode"
// is the only currently-used pairing; the interface is open for future
// modes (e.g. "maintenance").
type ModeOracle interface {
	// Mode returns the active mode string (e.g. "training_mode"). Empty
	// means "no override; treat all only_when_mode entries as inactive".
	Mode() string
}

// Scraper polls one service on a ticker. Start() is safe to call once.
type Scraper struct {
	Cfg   ServiceConfig
	Store *Store
	HTTP  *http.Client
	Mode  ModeOracle
	now   func() time.Time
}

// New returns a scraper with sensible defaults.
func New(cfg ServiceConfig, store *Store) *Scraper {
	if cfg.ScrapeIntervalS <= 0 {
		cfg.ScrapeIntervalS = 30
	}
	return &Scraper{
		Cfg:   cfg,
		Store: store,
		HTTP:  &http.Client{Timeout: time.Second},
		now:   time.Now,
	}
}

// WithMode wires a mode oracle. When the configured OnlyWhenMode doesn't
// match the current mode, ScrapeOnce is a no-op (the prior cached entry
// remains).
func (s *Scraper) WithMode(o ModeOracle) *Scraper {
	s.Mode = o
	return s
}

// Start runs the scrape loop until ctx is cancelled. Call in a goroutine.
func (s *Scraper) Start(ctx context.Context) {
	// Immediate first scrape so /health has something to report before the
	// first tick fires.
	s.ScrapeOnce(ctx)
	t := time.NewTicker(time.Duration(s.Cfg.ScrapeIntervalS) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.ScrapeOnce(ctx)
		}
	}
}

// ScrapeOnce performs a single scrape and writes the result to the store.
// On failure the entry is written with ScrapeOK=false so /health reflects
// current observability state rather than a stale snapshot.
//
// When the entry is mode-gated (OnlyWhenMode != ""), the scrape is skipped
// unless the mode oracle reports a matching mode. The store keeps any
// previous entry; we don't overwrite with a stale ScrapeOK=false in this
// case (the entry simply isn't ours to update right now).
func (s *Scraper) ScrapeOnce(ctx context.Context) {
	if s.Cfg.OnlyWhenMode != "" {
		want := s.Cfg.OnlyWhenMode
		got := ""
		if s.Mode != nil {
			got = s.Mode.Mode()
		}
		if got != want {
			return
		}
	}
	e := Scraped{
		Name:            s.Cfg.Name,
		URL:             s.Cfg.URL,
		ThresholdWarnMB: s.Cfg.ThresholdWarnMB,
		ThresholdCritMB: s.Cfg.ThresholdCritMB,
		LastScrapeTS:    s.now().Unix(),
	}
	req, err := http.NewRequestWithContext(ctx, "GET", s.Cfg.URL, nil)
	if err != nil {
		s.Store.Put(e)
		return
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		s.Store.Put(e)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		s.Store.Put(e)
		return
	}
	// Decode the entire body into a flat map first so we can split out the
	// canonical fields and preserve the rest under Extra.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		s.Store.Put(e)
		return
	}
	var r response
	if v, ok := raw["allocated_mb"]; ok {
		_ = json.Unmarshal(v, &r.AllocatedMB)
	}
	if v, ok := raw["reserved_mb"]; ok {
		_ = json.Unmarshal(v, &r.ReservedMB)
	}
	if v, ok := raw["max_allocated_mb"]; ok {
		_ = json.Unmarshal(v, &r.MaxAllocatedMB)
	}
	extra := map[string]json.RawMessage{}
	for k, v := range raw {
		if _, isCanon := canonicalFields[k]; isCanon {
			continue
		}
		extra[k] = v
	}
	e.ScrapeOK = true
	e.AllocatedMB = r.AllocatedMB
	e.ReservedMB = r.ReservedMB
	e.MaxAllocatedMB = r.MaxAllocatedMB
	if len(extra) > 0 {
		e.Extra = extra
	}
	if r.AllocatedMB > 0 {
		e.CacheOverheadPct = round2((r.ReservedMB/r.AllocatedMB - 1) * 100)
	}
	s.Store.Put(e)
}

// CreepRatio reports reserved/allocated. Returns 0 when allocated == 0 to
// avoid NaN propagation into the JSON.
func (s Scraped) CreepRatio() float64 {
	if s.AllocatedMB <= 0 {
		return 0
	}
	return s.ReservedMB / s.AllocatedMB
}

// Diagnostic string used in log messages.
func (s Scraped) String() string {
	return fmt.Sprintf("%s alloc=%.1fMB reserved=%.1fMB ratio=%.2f ok=%v",
		s.Name, s.AllocatedMB, s.ReservedMB, s.CreepRatio(), s.ScrapeOK)
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
