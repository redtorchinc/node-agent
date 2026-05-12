//go:build linux

package timesync

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Probe runs `chronyc tracking` first, falling back to `timedatectl
// show-timesync`. Returns nil when neither tool produces parseable output —
// the agent should treat that as "unknown" rather than synthesise a value.
func Probe(ctx context.Context) *Info {
	if i := probeChrony(ctx); i != nil {
		return i
	}
	if i := probeTimedatectl(ctx); i != nil {
		return i
	}
	return nil
}

func probeChrony(ctx context.Context) *Info {
	if _, err := exec.LookPath("chronyc"); err != nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "chronyc", "-n", "tracking").Output()
	if err != nil {
		return nil
	}
	info := &Info{Source: "chrony"}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := splitKV(line)
		if !ok {
			continue
		}
		switch k {
		case "Stratum":
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				info.Stratum = &n
			}
		case "System time":
			// "0.000123456 seconds slow of NTP time" or "fast of"
			ms := chronyOffsetMS(v)
			info.SkewMS = &ms
			info.Synced = true
		case "Last offset":
			// already covered by System time on newer chrony versions
		case "Update interval":
			// "65.4 seconds" — keep just the seconds as int.
			f, _ := strconv.ParseFloat(strings.Fields(strings.TrimSpace(v))[0], 64)
			n := int(f)
			info.LastUpdateS = &n
		}
	}
	if info.SkewMS == nil && info.Stratum == nil {
		return nil
	}
	return info
}

// chronyOffsetMS turns "0.000123456 seconds slow of NTP time" into 0.123 (ms).
// "fast of" is negative.
func chronyOffsetMS(v string) float64 {
	parts := strings.Fields(strings.TrimSpace(v))
	if len(parts) < 1 {
		return 0
	}
	f, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0
	}
	ms := f * 1000
	if strings.Contains(v, "fast of") {
		ms = -ms
	}
	return round2(ms)
}

func probeTimedatectl(ctx context.Context) *Info {
	if _, err := exec.LookPath("timedatectl"); err != nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "timedatectl", "show-timesync").Output()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return nil
	}
	info := &Info{Source: "timesyncd"}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := splitKV(line)
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "ServerName":
			info.Synced = v != ""
		case "PacketCount":
			// Treat any positive packet count as evidence of contact.
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				info.Synced = true
			}
		}
	}
	if !info.Synced {
		return nil
	}
	return info
}

func splitKV(line string) (string, string, bool) {
	// chronyc separator is ":"; timedatectl uses "=".
	for _, sep := range []string{":", "="} {
		if i := strings.Index(line, sep); i > 0 {
			return strings.TrimSpace(line[:i]), line[i+1:], true
		}
	}
	return "", "", false
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
