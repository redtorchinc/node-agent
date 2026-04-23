//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	systemdUnitPath = "/etc/systemd/system/rt-node-agent.service"
	agentUser       = "rt-agent"
	agentGroup      = "rt-agent"
	configDir       = "/etc/rt-node-agent"
	logDir          = "/var/log/rt-node-agent"
)

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
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=%s
EnvironmentFile=-%s/env

[Install]
WantedBy=multi-user.target
`

func install() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("install requires root (try: sudo rt-node-agent install)")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)

	if err := ensureUser(agentUser); err != nil {
		return err
	}
	if err := ensureDir(configDir, 0o755, "root", "root"); err != nil {
		return err
	}
	if err := ensureDir(logDir, 0o755, agentUser, agentGroup); err != nil {
		return err
	}

	unit := fmt.Sprintf(unitTemplate, exe, agentUser, agentGroup, logDir, configDir)
	if err := os.WriteFile(systemdUnitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "--now", "rt-node-agent"); err != nil {
		return err
	}
	fmt.Println("rt-node-agent installed and started (systemd)")
	return nil
}

func uninstall() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("uninstall requires root")
	}
	// Ignore errors here — we want uninstall to be tolerant of partial installs.
	_ = run("systemctl", "disable", "--now", "rt-node-agent")
	if err := os.Remove(systemdUnitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = run("systemctl", "daemon-reload")
	// Config + token preserved by design (see PLAN.md §M9).
	fmt.Println("rt-node-agent uninstalled (config preserved at " + configDir + ")")
	return nil
}

func startSvc() error { return run("systemctl", "start", "rt-node-agent") }
func stopSvc() error  { return run("systemctl", "stop", "rt-node-agent") }

func status() (State, error) {
	if _, err := os.Stat(systemdUnitPath); os.IsNotExist(err) {
		return StateNotInstalled, nil
	}
	out, err := exec.Command("systemctl", "is-active", "rt-node-agent").CombinedOutput()
	switch strings.TrimSpace(string(out)) {
	case "active":
		return StateRunning, nil
	case "inactive", "failed":
		return StateStopped, nil
	}
	if err != nil {
		return StateUnknown, nil
	}
	return StateUnknown, nil
}

func ensureUser(name string) error {
	if _, err := exec.Command("id", name).CombinedOutput(); err == nil {
		return nil
	}
	return run("useradd",
		"--system",
		"--no-create-home",
		"--shell", "/usr/sbin/nologin",
		name,
	)
}

func ensureDir(path string, mode os.FileMode, owner, group string) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	if owner != "" {
		if err := run("chown", owner+":"+group, path); err != nil {
			return err
		}
	}
	return nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
