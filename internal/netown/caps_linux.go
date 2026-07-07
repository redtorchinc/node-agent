//go:build linux

package netown

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Capability bit numbers from <linux/capability.h>. Reading another
// user's /proc/<pid>/{fd,exe} needs both: CAP_SYS_PTRACE to pass the
// ptrace access-mode check, CAP_DAC_READ_SEARCH to bypass the 0500
// directory permissions.
const (
	capDACReadSearch = 2
	capSysPtrace     = 19
)

// attributionHint explains, when non-empty, why socket→pid attribution
// fails for other users' processes and how to fix it. Root, or the
// AmbientCapabilities grant the installer applies as of v0.3.1 (issue
// #23), both silence it.
func attributionHint() string {
	if os.Geteuid() == 0 {
		return ""
	}
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return ""
	}
	mask, ok := capEffMask(string(data))
	if !ok {
		return ""
	}
	missing := missingAttributionCaps(mask)
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("agent lacks %s — re-run the installer (v0.3.1+) or see docs/api/network-flows.md §Privileges (Linux)",
		strings.Join(missing, "+"))
}

// capEffMask extracts the effective-capability bitmask from a
// /proc/self/status body.
func capEffMask(status string) (uint64, bool) {
	for _, line := range strings.Split(status, "\n") {
		v, ok := strings.CutPrefix(line, "CapEff:")
		if !ok {
			continue
		}
		mask, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
		if err != nil {
			return 0, false
		}
		return mask, true
	}
	return 0, false
}

// missingAttributionCaps lists which of the two required capabilities
// the effective mask lacks.
func missingAttributionCaps(mask uint64) []string {
	var missing []string
	if mask&(1<<capDACReadSearch) == 0 {
		missing = append(missing, "CAP_DAC_READ_SEARCH")
	}
	if mask&(1<<capSysPtrace) == 0 {
		missing = append(missing, "CAP_SYS_PTRACE")
	}
	return missing
}
