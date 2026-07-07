package service

// systemd unit rendering for the Linux install path. Untagged (compiled
// on every OS) so renderUnit stays testable from any dev platform; only
// systemd.go, behind //go:build linux, actually writes the result.

import "fmt"

const (
	agentUser  = "rt-agent"
	agentGroup = "rt-agent"
	configDir  = "/etc/rt-node-agent"
	logDir     = "/var/log/rt-node-agent"
)

// unitTemplate deliberately omits two hardening directives:
//
//   - NoNewPrivileges: it blocks setuid, which kills the sudo → systemctl
//     path POST /actions/service depends on (the sudoers drop-in scoped to
//     rt-vllm-*.service is the safety rail there, not NNP).
//   - CapabilityBoundingSet: limiting the bounding set to the two
//     attribution caps would also strip the sudo'd systemctl of the root
//     capabilities it needs, for the same reason.
const unitTemplate = `[Unit]
Description=RedTorch Node Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s run
Restart=on-failure
RestartSec=5
User=%s
Group=%s
%sProtectSystem=strict
ProtectHome=true
ReadWritePaths=%s
EnvironmentFile=-%s/env

[Install]
WantedBy=multi-user.target
`

// netCapsLine grants the socket→pid join in /network/* the right to read
// other users' /proc/<pid>/{fd,exe}: CAP_SYS_PTRACE passes the ptrace
// access-mode check, CAP_DAC_READ_SEARCH bypasses the 0500 directory
// perms (issue #23). Gated on network.flows_enabled at install time.
const netCapsLine = "AmbientCapabilities=CAP_SYS_PTRACE CAP_DAC_READ_SEARCH\n"

// renderUnit produces the systemd unit body. netCaps toggles the
// attribution capability grant.
func renderUnit(exe string, netCaps bool) string {
	caps := ""
	if netCaps {
		caps = netCapsLine
	}
	return fmt.Sprintf(unitTemplate, exe, agentUser, agentGroup, caps, logDir, configDir)
}
