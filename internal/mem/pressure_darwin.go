//go:build darwin

package mem

import (
	"bytes"
	"os/exec"
	"strconv"
	"strings"

	gmem "github.com/shirou/gopsutil/v3/mem"
)

// probePressure reads `kern.memorystatus_vm_pressure_level` via sysctl.
// Apple's libdispatch headers define three values:
//
//	1 = DISPATCH_MEMORYPRESSURE_NORMAL
//	2 = DISPATCH_MEMORYPRESSURE_WARN
//	4 = DISPATCH_MEMORYPRESSURE_CRITICAL  (3 is intentionally skipped — it's a bitmask, not sequential)
//
// We translate to the same {normal, some, full} vocabulary the Linux
// path uses so the wire contract is identical across OSes. Returns ""
// if sysctl is missing or unparseable — preserves the "silence beats
// fabrication" rule that's baked into degraded_reasons.
func probePressure() string {
	cmd := exec.Command("/usr/sbin/sysctl", "-n", "kern.memorystatus_vm_pressure_level")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	v, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil {
		return ""
	}
	switch v {
	case 1:
		return "normal"
	case 2:
		return "some"
	case 4:
		return "full"
	}
	return ""
}

// probePSI is Linux-only — PSI is a Linux kernel feature, no macOS equivalent.
func probePSI() *PSI { return nil }

// probeSwapCounters reads darwin's pages-in / pages-out via gopsutil's
// mem.SwapMemory, which surfaces the Mach VM Sin / Sout counters on
// darwin. Same semantics as Linux's /proc/vmstat pswpin/pswpout: total
// pages swapped since boot. Returns (0,0,false) when the counters
// aren't available.
func probeSwapCounters() (uint64, uint64, bool) {
	s, err := gmem.SwapMemory()
	if err != nil || s == nil {
		return 0, 0, false
	}
	if s.Sin == 0 && s.Sout == 0 {
		// No swap activity since boot — surface as absent rather than 0,0
		// so consumers can distinguish "no events" from "kernel doesn't
		// expose this counter on this build".
		return 0, 0, false
	}
	return s.Sin, s.Sout, true
}

// probeHugePages is Linux-only (huge-page semantics differ on darwin).
func probeHugePages() (int64, int64, bool) { return 0, 0, false }
