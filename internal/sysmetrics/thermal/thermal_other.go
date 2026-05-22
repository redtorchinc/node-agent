//go:build !darwin

package thermal

import "context"

// start is a no-op on non-darwin platforms. The snapshot stays empty,
// which the health composer reads as "no thermal overlay to apply" and
// falls back to the platform's native temp sources (hwmon on Linux,
// WMI on Windows, nvidia-smi for discrete GPUs everywhere).
func (p *Probe) start(_ context.Context) {}
