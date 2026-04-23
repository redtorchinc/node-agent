package gpu

import "context"

// Noop is the fallback probe used when no supported GPU stack is present.
type Noop struct{}

// NewNoop returns a probe that always reports zero GPUs.
func NewNoop() *Noop { return &Noop{} }

// Probe implements Probe.
func (Noop) Probe(_ context.Context) ([]GPU, error) { return []GPU{}, nil }
