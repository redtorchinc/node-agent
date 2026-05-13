// Package ollama integrates with the local Ollama server on port 11434.
//
// The agent does two things here: (a) mirror /api/ps into the /health
// payload so the case-manager can see which models are resident and for
// how long, and (b) expose POST /actions/unload-model to free a named
// model on demand. All talks to Ollama use a 2s timeout per SPEC §HTTP API.
package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Info is the /health.ollama payload.
type Info struct {
	Up        bool     `json:"up"`
	Endpoint  string   `json:"endpoint"`
	Models    []Model  `json:"models"`
	Runners   []Runner `json:"runners"`
	LastProbe int64    `json:"last_probe_ts"`
}

// Model mirrors the fields we care about from Ollama's /api/ps response.
// QueuedRequests is 0 when the serving Ollama doesn't expose the field —
// the agent treats absent/zero as "no evidence of queue" and refuses to
// fire ollama_runner_stuck without it.
type Model struct {
	Name           string `json:"name"`
	SizeMB         int64  `json:"size_mb"`
	Processor      string `json:"processor"`
	Context        int    `json:"context"`
	UntilS         int64  `json:"until_s"`
	QueuedRequests int    `json:"queued_requests"`
}

// Runner describes one `ollama runner` subprocess.
type Runner struct {
	PID    int     `json:"pid"`
	CPUPct float64 `json:"cpu_pct"`
	RSSMB  int64   `json:"rss_mb"`
}

// Client is a minimal HTTP client for the local Ollama server. It caches
// /api/ps responses for 5s so that a tight loop of /health calls (the
// backend polls on every dispatch decision) doesn't hammer Ollama.
type Client struct {
	Endpoint string
	HTTP     *http.Client

	now func() time.Time // overridable in tests

	mu        sync.Mutex
	cached    *Info
	cachedAt  time.Time
	cacheTTL  time.Duration
}

// NewClient returns a Client targeting the given endpoint (e.g. http://localhost:11434).
// If endpoint is empty, http://localhost:11434 is used.
func NewClient(endpoint string) *Client {
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	return &Client{
		Endpoint: endpoint,
		HTTP:     &http.Client{Timeout: 2 * time.Second},
		now:      time.Now,
		cacheTTL: 5 * time.Second,
	}
}

// CacheTTL is the /api/ps response cache duration. Exposed so the platforms
// adapter can surface it as probe_interval_s in /health.
func (c *Client) CacheTTL() time.Duration { return c.cacheTTL }

// Probe returns the current Ollama state. On error (including timeout) the
// returned Info reports Up=false so the caller can fold it straight into
// /health without special-casing.
func (c *Client) Probe(ctx context.Context) Info {
	c.mu.Lock()
	if c.cached != nil && c.now().Sub(c.cachedAt) < c.cacheTTL {
		cached := *c.cached
		c.mu.Unlock()
		return cached
	}
	c.mu.Unlock()

	info := Info{
		Endpoint:  c.Endpoint,
		Models:    []Model{},
		Runners:   []Runner{},
		LastProbe: c.now().Unix(),
	}

	models, err := c.fetchPs(ctx)
	if err == nil {
		info.Up = true
		info.Models = models
	}

	info.Runners = probeRunners()

	c.mu.Lock()
	c.cached = &info
	c.cachedAt = c.now()
	c.mu.Unlock()
	return info
}

type psResp struct {
	Models []psModel `json:"models"`
}

type psModel struct {
	Name      string `json:"name"`
	Model     string `json:"model"`
	Size      int64  `json:"size"`
	ExpiresAt string `json:"expires_at"`
	Details   struct {
		Format  string `json:"format"`
		Context int    `json:"context_length"`
	} `json:"details"`
	// Ollama's /api/ps also returns size_vram. A 100%-GPU model has
	// size == size_vram; otherwise it's a split. We compute "processor"
	// heuristically from these two values.
	SizeVRAM int64 `json:"size_vram"`
	// QueuedRequests is not present on all Ollama versions. Missing JSON
	// keys leave the int zero-valued, which the degraded evaluator reads
	// as "no queue visible" — matches the intent of not false-positiving.
	QueuedRequests int `json:"queued_requests"`
}

func (c *Client) fetchPs(ctx context.Context) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.Endpoint+"/api/ps", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("ollama /api/ps: http %d", resp.StatusCode)
	}
	var p psResp
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, err
	}
	now := c.now()
	out := make([]Model, 0, len(p.Models))
	for _, m := range p.Models {
		name := m.Name
		if name == "" {
			name = m.Model
		}
		until := int64(0)
		if m.ExpiresAt != "" {
			if t, err := time.Parse(time.RFC3339, m.ExpiresAt); err == nil {
				until = int64(t.Sub(now).Seconds())
				if until < 0 {
					until = 0
				}
			}
		}
		out = append(out, Model{
			Name:           name,
			SizeMB:         m.Size / 1024 / 1024,
			Processor:      processor(m.Size, m.SizeVRAM),
			Context:        m.Details.Context,
			UntilS:         until,
			QueuedRequests: m.QueuedRequests,
		})
	}
	return out, nil
}

func processor(total, vram int64) string {
	if total <= 0 {
		return ""
	}
	if vram >= total {
		return "100% GPU"
	}
	if vram == 0 {
		return "100% CPU"
	}
	pct := int(float64(vram) / float64(total) * 100)
	return fmt.Sprintf("%d%% GPU/%d%% CPU", pct, 100-pct)
}

// ErrNoModel is returned by Unload when the model is not currently loaded.
// Callers typically treat this as a successful no-op (idempotent unload).
var ErrNoModel = errors.New("model not loaded")
