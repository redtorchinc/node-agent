//go:build darwin

package timesync

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Probe shells `sntp -t 1 time.apple.com` if available. Many Macs disable
// outbound NTP queries except by the system daemon, so this is best-effort.
// Returns nil when nothing parses cleanly.
func Probe(ctx context.Context) *Info {
	if _, err := exec.LookPath("sntp"); err != nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "sntp", "-t", "1", "time.apple.com").Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	// sntp prints e.g. "+0.000123 +/- 0.011 time.apple.com 17.253.84.253"
	parts := strings.Fields(string(out))
	if len(parts) == 0 {
		return nil
	}
	info := &Info{Source: "sntp", Synced: true}
	return info
}
