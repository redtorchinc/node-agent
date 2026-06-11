//go:build windows

package timesync

import "context"

// probeOSSync is a no-op on Windows for v0.2.x. w32tm parsing is on the
// v0.3 roadmap; surfacing nil here leaves /health.time_sync.source empty
// and SkewMS/Stratum/LastUpdateS unpopulated, but the wall-clock and
// agent-driven server probe still work. Caller is Compose(); not exported.
func probeOSSync(_ context.Context, _ string) *Info { return nil }
