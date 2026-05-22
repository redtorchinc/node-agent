package timesync

import (
	"context"
	"testing"
	"time"
)

// TestCompose_AlwaysReturnsWallClock verifies the v0.2.x invariant: the
// caller can always trust NowUnixNS, NowISO, and TZOffsetS even when
// the OS sync daemon probe returns nil and no server probe is wired.
// This is the field the case-manager subtracts from its own clock for
// cross-node offset calculation.
func TestCompose_AlwaysReturnsWallClock(t *testing.T) {
	info := Compose(context.Background(), nil)
	if info == nil {
		t.Fatal("Compose returned nil")
	}
	if info.NowUnixNS == 0 {
		t.Error("NowUnixNS = 0, want now()")
	}
	// NowISO should match RFC3339Nano UTC and be within a few seconds
	// of NowUnixNS (we generated both in the same call).
	parsed, err := time.Parse(time.RFC3339Nano, info.NowISO)
	if err != nil {
		t.Fatalf("NowISO %q is not RFC3339Nano: %v", info.NowISO, err)
	}
	if d := parsed.UnixNano() - info.NowUnixNS; d > int64(time.Second) || d < -int64(time.Second) {
		t.Errorf("NowISO and NowUnixNS disagree by %d ns", d)
	}
}

// TestCompose_AttachesServerProbe verifies that when a ServerProbe is
// passed, its Snapshot is wired in. Doesn't depend on a real probe
// having succeeded — the empty-snapshot case is the cold-start state
// and still needs to be observable in /health.
func TestCompose_AttachesServerProbe(t *testing.T) {
	probe := NewServerProbe("time.example.invalid")
	info := Compose(context.Background(), probe)
	if info.Server == nil {
		t.Fatal("Compose with non-nil ServerProbe must populate Info.Server")
	}
	if info.Server.Host != "time.example.invalid" {
		t.Errorf("Server.Host = %q, want time.example.invalid", info.Server.Host)
	}
	if info.Server.ProbeIntervalS == 0 {
		t.Errorf("Server.ProbeIntervalS = 0, want %d", int64(ServerProbeInterval/time.Second))
	}
}

// TestCompose_NoServerProbe verifies the opt-out path: when no server
// probe is configured, the Server subfield is nil — the JSON encoder
// then omits it entirely (omitempty). The case-manager reads that as
// "operator disabled the agent's own NTP probe."
func TestCompose_NoServerProbe(t *testing.T) {
	info := Compose(context.Background(), nil)
	if info.Server != nil {
		t.Errorf("Server = %+v, want nil when no probe is configured", info.Server)
	}
}
