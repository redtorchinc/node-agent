package service

import "strings"
import "testing"

func TestRenderUnit_WithNetCaps(t *testing.T) {
	u := renderUnit("/usr/local/bin/rt-node-agent", true)

	for _, want := range []string{
		"ExecStart=/usr/local/bin/rt-node-agent run",
		"User=rt-agent",
		"Group=rt-agent",
		"AmbientCapabilities=CAP_SYS_PTRACE CAP_DAC_READ_SEARCH",
		"ProtectSystem=strict",
		"ProtectHome=true",
		"ReadWritePaths=/var/log/rt-node-agent",
		"EnvironmentFile=-/etc/rt-node-agent/env",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
	if strings.Contains(u, "%!") {
		t.Errorf("unit has a format-verb mismatch:\n%s", u)
	}
}

func TestRenderUnit_WithoutNetCaps(t *testing.T) {
	u := renderUnit("/usr/local/bin/rt-node-agent", false)
	if strings.Contains(u, "AmbientCapabilities") {
		t.Errorf("caps-off unit must not grant capabilities:\n%s", u)
	}
	if !strings.Contains(u, "ProtectSystem=strict") {
		t.Errorf("hardening lines must survive the caps-off branch:\n%s", u)
	}
}

// The sudo → systemctl path of POST /actions/service requires setuid and
// the full root capability set, so neither directive may reappear in any
// branch (see the unitTemplate comment).
func TestRenderUnit_NoSudoBreakingDirectives(t *testing.T) {
	for _, netCaps := range []bool{true, false} {
		u := renderUnit("/usr/local/bin/rt-node-agent", netCaps)
		if strings.Contains(u, "NoNewPrivileges") {
			t.Errorf("netCaps=%v: NoNewPrivileges blocks the sudo path:\n%s", netCaps, u)
		}
		if strings.Contains(u, "CapabilityBoundingSet") {
			t.Errorf("netCaps=%v: CapabilityBoundingSet strips sudo'd systemctl:\n%s", netCaps, u)
		}
	}
}
