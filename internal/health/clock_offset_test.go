package health

import (
	"testing"
	"time"

	"github.com/redtorchinc/node-agent/internal/config"
	"github.com/redtorchinc/node-agent/internal/sysmetrics/timesync"
)

// withServerOffset is a small helper that returns a clean report with
// time_sync.server.offset_ms set to v. Other fields stay at their
// clean-report defaults so the test isolates one reason at a time.
func withServerOffset(now time.Time, v float64) Report {
	r := cleanReport(now)
	r.TimeSync = &timesync.Info{
		Server: &timesync.ServerInfo{
			Host:     "time.cloudflare.com",
			OffsetMS: &v,
		},
	}
	return r
}

func TestEvaluate_ClockOffsetHigh_PositiveFires(t *testing.T) {
	now := time.Unix(1713820000, 0)
	deg, reasons := Evaluate(withServerOffset(now, 250), config.Config{}, now)
	if deg {
		t.Errorf("clock_offset_high is soft, must not set degraded=true")
	}
	if !contains(reasons, ReasonClockOffsetHigh) {
		t.Errorf("offset_ms=250 should fire clock_offset_high; got %v", reasons)
	}
}

func TestEvaluate_ClockOffsetHigh_NegativeFires(t *testing.T) {
	now := time.Unix(1713820000, 0)
	_, reasons := Evaluate(withServerOffset(now, -500), config.Config{}, now)
	if !contains(reasons, ReasonClockOffsetHigh) {
		t.Errorf("offset_ms=-500 should fire clock_offset_high; got %v", reasons)
	}
}

func TestEvaluate_ClockOffsetHigh_BelowThresholdSilent(t *testing.T) {
	now := time.Unix(1713820000, 0)
	_, reasons := Evaluate(withServerOffset(now, 50), config.Config{}, now)
	if contains(reasons, ReasonClockOffsetHigh) {
		t.Errorf("offset_ms=50 should be below the 100ms threshold; got %v", reasons)
	}
}

// Critical: silence beats fabrication. When the agent hasn't been
// configured with a timeserver (Server == nil), or the first probe has
// not yet completed (Server.OffsetMS == nil), we must NOT emit
// clock_offset_high — operators reading "node X has clock_offset_high"
// would assume a real reading exists.
func TestEvaluate_ClockOffsetHigh_SilentWhenNoServerConfigured(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.TimeSync = &timesync.Info{} // wall-clock-only Info, no Server
	_, reasons := Evaluate(r, config.Config{}, now)
	if contains(reasons, ReasonClockOffsetHigh) {
		t.Errorf("clock_offset_high must be silent when Server is nil; got %v", reasons)
	}
}

func TestEvaluate_ClockOffsetHigh_SilentBeforeFirstProbe(t *testing.T) {
	now := time.Unix(1713820000, 0)
	r := cleanReport(now)
	r.TimeSync = &timesync.Info{
		Server: &timesync.ServerInfo{
			Host: "time.cloudflare.com",
			// OffsetMS deliberately nil → no successful probe yet
			Error: "timeout",
		},
	}
	_, reasons := Evaluate(r, config.Config{}, now)
	if contains(reasons, ReasonClockOffsetHigh) {
		t.Errorf("clock_offset_high must be silent before first successful probe; got %v", reasons)
	}
}
