//go:build windows

package timesync

import "context"

// Probe is a no-op on Windows for v0.2.0. w32tm parsing is on the v0.3
// roadmap; surfacing nil here just means /health.time_sync is omitted.
func Probe(_ context.Context) *Info { return nil }
