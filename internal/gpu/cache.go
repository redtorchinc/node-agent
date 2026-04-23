package gpu

import (
	"context"
	"sync"
	"time"
)

// CachedProbe wraps another Probe with a TTL cache. Fresh calls return the
// cached value without hitting the underlying probe; stale calls run the
// probe under a mutex (concurrent /health requests are serialized rather
// than piling up on system_profiler / nvidia-smi). On probe error the
// previous cached value is returned rather than an empty list, so a single
// transient failure doesn't flip GPU visibility off.
//
// Motivation: macOS system_profiler reliably spikes to 1-2s under host
// load, which blew the case-manager's 2s client timeout on /health. GPU
// topology doesn't change second-to-second, so caching is safe.
type CachedProbe struct {
	inner Probe
	ttl   time.Duration

	mu   sync.Mutex
	last []GPU
	at   time.Time
}

// NewCached wraps p in a TTL cache.
func NewCached(p Probe, ttl time.Duration) *CachedProbe {
	return &CachedProbe{inner: p, ttl: ttl}
}

// Probe implements Probe.
func (c *CachedProbe) Probe(ctx context.Context) ([]GPU, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.at.IsZero() && time.Since(c.at) < c.ttl {
		return append([]GPU(nil), c.last...), nil
	}

	gpus, err := c.inner.Probe(ctx)
	if err != nil {
		// Serve stale rather than drop GPU visibility on a transient failure.
		if !c.at.IsZero() {
			return append([]GPU(nil), c.last...), nil
		}
		return nil, err
	}
	c.last = gpus
	c.at = time.Now()
	return append([]GPU(nil), gpus...), nil
}
