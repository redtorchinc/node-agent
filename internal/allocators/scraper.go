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

	// ThresholdWarnMB/CritMB are copied from config so the /health evaluator
	// can decide vram_service_creep_* without re-reading the config struct.
	ThresholdWarnMB int64 `json:"-"`
	ThresholdCritMB int64 `json:"-"`
}

// response is the expected shape emitted by a cooperating service.
type response struct {
	AllocatedMB    float64 `json:"allocated_mb"`
	ReservedMB     float64 `json:"reserved_mb"`
	MaxAllocatedMB float64 `json:"max_allocated_mb"`
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

// Scraper polls one service on a ticker. Start() is safe to call once.
type Scraper struct {
	Cfg   ServiceConfig
	Store *Store
	HTTP  *http.Client
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
func (s *Scraper) ScrapeOnce(ctx context.Context) {
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
	var r response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		s.Store.Put(e)
		return
	}
	e.ScrapeOK = true
	e.AllocatedMB = r.AllocatedMB
	e.ReservedMB = r.ReservedMB
	e.MaxAllocatedMB = r.MaxAllocatedMB
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
