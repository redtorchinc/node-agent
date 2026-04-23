package gpu

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeProbe struct {
	calls int
	ret   []GPU
	err   error
}

func (f *fakeProbe) Probe(_ context.Context) ([]GPU, error) {
	f.calls++
	return f.ret, f.err
}

func TestCachedProbe_ServesFromCacheWithinTTL(t *testing.T) {
	inner := &fakeProbe{ret: []GPU{{Index: 0, Name: "A"}}}
	c := NewCached(inner, 5*time.Second)

	_, _ = c.Probe(context.Background())
	_, _ = c.Probe(context.Background())
	_, _ = c.Probe(context.Background())

	if inner.calls != 1 {
		t.Fatalf("want 1 inner call, got %d", inner.calls)
	}
}

func TestCachedProbe_ServesStaleOnError(t *testing.T) {
	inner := &fakeProbe{ret: []GPU{{Index: 0, Name: "A"}}}
	c := NewCached(inner, 1*time.Nanosecond)

	// First call populates cache.
	gpus, err := c.Probe(context.Background())
	if err != nil || len(gpus) != 1 {
		t.Fatalf("first call: %v %+v", err, gpus)
	}

	// TTL trivially expired. Next call will re-probe, but inner now errors.
	inner.ret = nil
	inner.err = errors.New("nvidia-smi exploded")
	gpus, err = c.Probe(context.Background())
	if err != nil {
		t.Errorf("want nil err (serve stale), got %v", err)
	}
	if len(gpus) != 1 || gpus[0].Name != "A" {
		t.Errorf("want stale [A], got %+v", gpus)
	}
}

func TestCachedProbe_ErrorOnColdCache(t *testing.T) {
	inner := &fakeProbe{err: errors.New("first-call failure")}
	c := NewCached(inner, 5*time.Second)

	_, err := c.Probe(context.Background())
	if err == nil {
		t.Fatal("want error on cold-cache failure, got nil")
	}
}
