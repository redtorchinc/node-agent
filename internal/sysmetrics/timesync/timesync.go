// Package timesync surfaces clock-sync state under /health.time_sync.
//
// Two independent sources fold into the wire shape:
//
//  1. Local OS sync daemon — chronyc / timedatectl on Linux, sntp on
//     macOS, w32tm on Windows (not yet implemented). Tells us whether the
//     host's own NTP machinery believes it is in sync. This is the
//     historical v0.2.0 behavior.
//
//  2. Agent-driven NTP probe — when config.timesync.server is set, the
//     agent itself queries that server on a background loop and exposes
//     local-vs-server offset under .server. This is independent of the
//     OS daemon and lets the case-manager rank nodes against a single
//     reference clock without trusting each host's sync daemon
//     individually.
//
// In addition the composer always populates the node's own wall-clock at
// /health composition time (now_unix_ns / now_iso) plus timezone. The
// case-manager subtracts its own clock minus this value (minus RTT/2) to
// compute cross-node offsets — which is the primary use case driving
// the v0.2.x extension.
package timesync

// Info is /health.time_sync. The Reporter populates this on every
// /health call: NowUnixNS and Now* are cheap (in-process time.Now()),
// OS sync fields come from a synchronous per-OS probe, and Server is
// the cached snapshot from the background ServerProbe runner.
//
// All optional fields are pointers so that "unknown" stays
// distinguishable from "zero" — degraded_reasons evaluation depends on
// this distinction (silence beats fabrication).
type Info struct {
	// NowUnixNS is the node's wall clock at /health composition time,
	// in nanoseconds since the Unix epoch. Always populated. Primary
	// cross-node comparison primitive: the case-manager subtracts this
	// from its own clock to compute per-node offsets.
	NowUnixNS int64 `json:"now_unix_ns"`

	// NowISO is NowUnixNS rendered as RFC3339Nano UTC, e.g.
	// "2026-05-22T14:00:01.123456789Z". Always populated. Redundant with
	// NowUnixNS but useful for log dashboards that can't render ns
	// timestamps natively.
	NowISO string `json:"now_iso"`

	// TZName is the local-time zone name (e.g. "UTC", "America/Los_Angeles").
	// May be empty on hosts where Go can't resolve the zone name.
	TZName string `json:"tz_name,omitempty"`

	// TZOffsetS is the current offset from UTC in seconds at NowUnixNS.
	// Always populated (0 when host is on UTC).
	TZOffsetS int `json:"tz_offset_s"`

	// Source identifies the OS sync daemon, when one is detected.
	// "chrony" | "timesyncd" | "sntp" | "w32tm" | "" (not detected).
	Source string `json:"source,omitempty"`

	// Synced is whether the OS sync daemon believes the local clock is
	// synchronised. False when Source is empty (no daemon detected).
	Synced bool `json:"synced"`

	// SkewMS is the OS daemon's view of local offset from its reference.
	// Sign convention matches chronyc: negative = local clock behind.
	// nil when no OS daemon reading is available.
	SkewMS *float64 `json:"skew_ms,omitempty"`

	// Stratum is the OS daemon's effective stratum (1 = direct GPS/atomic,
	// 2-15 = increasing distance from authoritative source).
	Stratum *int `json:"stratum,omitempty"`

	// LastUpdateS is seconds since the OS daemon's last successful sync.
	LastUpdateS *int `json:"last_update_s,omitempty"`

	// Server is the result of the agent's own NTP probe against the
	// host configured in timesync.server. nil when timesync.server is
	// empty in config (the default-off state). When non-nil, the agent
	// has attempted at least one probe and the embedded fields reflect
	// the most recent result (which may itself be an error).
	Server *ServerInfo `json:"server,omitempty"`
}

// ServerInfo is the wire shape under /health.time_sync.server.
//
// Populated from a background ServerProbe goroutine. OffsetMS uses the
// convention LOCAL - SERVER (positive = local clock is ahead of the
// reference). RTTMS is the network round-trip excluding the server's
// own queue (standard NTP formula).
type ServerInfo struct {
	// Host is the configured timesync.server value (e.g.
	// "time.cloudflare.com"). Echoed back so the case-manager doesn't
	// need to re-read the config to know which reference clock this
	// node is using.
	Host string `json:"host"`

	// OffsetMS is local clock minus server clock at last successful
	// probe. Positive = local is ahead. nil when no successful probe
	// has completed yet (cold start or persistent error).
	OffsetMS *float64 `json:"offset_ms,omitempty"`

	// RTTMS is the network round-trip in milliseconds at last
	// successful probe, computed as (t4-t1) - (t3-t2) per NTP RFC 5905.
	RTTMS *float64 `json:"rtt_ms,omitempty"`

	// Stratum is the server's advertised stratum.
	Stratum *int `json:"stratum,omitempty"`

	// LastProbeAgeS is seconds since the last probe attempt (success
	// or failure). nil before the first attempt completes.
	LastProbeAgeS *int `json:"last_probe_age_s,omitempty"`

	// ProbeIntervalS is the background refresh cadence in seconds.
	// Self-describing so the case-manager knows how stale the
	// reading might be.
	ProbeIntervalS int64 `json:"probe_interval_s"`

	// Error is the last probe error (empty when the last probe
	// succeeded). Operators reading /health can tell at a glance
	// whether the configured server is reachable.
	Error string `json:"error,omitempty"`
}
