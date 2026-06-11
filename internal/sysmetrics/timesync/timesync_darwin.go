//go:build darwin

package timesync

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// probeOSSync shells `sntp -t 1 <server>` if available. The server is the
// configured timesync.server when set — on egress-less fleets the hardcoded
// public fallback (time.apple.com) is unreachable, which used to report
// synced=false on Macs whose clocks were perfectly disciplined by an
// internal NTP server (FIX 2026-06-11). Many Macs disable outbound NTP
// queries except by the system daemon, so this is best-effort. Returns nil
// when nothing parses cleanly. Caller is Compose(); not exported.
func probeOSSync(ctx context.Context, server string) *Info {
	if _, err := exec.LookPath("sntp"); err != nil {
		return nil
	}
	if server == "" {
		server = "time.apple.com"
	}
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "sntp", "-t", "1", server).Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	// sntp prints e.g. "+0.000123 +/- 0.011 time.apple.com 17.253.84.253"
	parts := strings.Fields(string(out))
	if len(parts) == 0 {
		return nil
	}
	info := &Info{Source: "sntp", Synced: true}
	// parts[0] is the offset in seconds — parse into SkewMS so the darwin
	// OS-sync reading is quantitative, not just a boolean (FIX 2026-06-11;
	// previously dropped on the floor).
	if off, perr := strconv.ParseFloat(parts[0], 64); perr == nil {
		ms := off * 1000
		info.SkewMS = &ms
	}
	return info
}
