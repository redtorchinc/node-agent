package timesync

import (
	"context"
	"time"
)

// Compose builds a complete Info ready for /health.time_sync. Always
// returns a non-nil pointer.
//
// The wall-clock fields (NowUnixNS, NowISO, TZName, TZOffsetS) are
// populated from the calling goroutine's time.Now() — primary use case
// is the case-manager subtracting its own clock to derive a per-node
// offset, so the timestamp must reflect when this /health response was
// composed, NOT some earlier cached value.
//
// The OS-sync fields (Source, Synced, SkewMS, Stratum, LastUpdateS)
// come from the platform-specific probeOSSync (chronyc/timedatectl on
// Linux, sntp on darwin, no-op on Windows). They are best-effort:
// platforms that can't probe leave the optional fields nil and Source
// empty — degraded.go gates on that nil-vs-value distinction.
//
// The Server field is non-nil exactly when serverProbe is non-nil
// (timesync.server configured). The snapshot may itself report an
// error or have nil OffsetMS if no probe has succeeded yet — the
// composer surfaces it raw.
func Compose(ctx context.Context, serverProbe *ServerProbe) *Info {
	now := time.Now()
	zone, offsetS := now.Zone()
	info := &Info{
		NowUnixNS: now.UnixNano(),
		NowISO:    now.UTC().Format(time.RFC3339Nano),
		TZName:    zone,
		TZOffsetS: offsetS,
	}
	if os := probeOSSync(ctx); os != nil {
		info.Source = os.Source
		info.Synced = os.Synced
		info.SkewMS = os.SkewMS
		info.Stratum = os.Stratum
		info.LastUpdateS = os.LastUpdateS
	}
	if serverProbe != nil {
		info.Server = serverProbe.Snapshot()
	}
	return info
}
