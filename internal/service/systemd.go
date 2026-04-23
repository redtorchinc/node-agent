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
	if err := writeConfigExample(configDir); err != nil {
		return fmt.Errorf("write config.yaml.example: %w", err)
	}

	// Token bootstrap: if no token exists, generate one. Otherwise leave it
	// alone — reinstalls must not rotate secrets.
	tokenPath := filepath.Join(configDir, "token")
	newToken, err := ensureTokenLinux(tokenPath)
	if err != nil {
		return fmt.Errorf("bootstrap token: %w", err)
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
	if newToken != "" {
		fmt.Println()
		fmt.Println("A bearer token was generated and written to " + tokenPath + ":")
		fmt.Println("  " + newToken)
		fmt.Println()
		fmt.Println("The case-manager backend will use this token for POST /actions/*.")
		fmt.Println("To rotate: write a new value to the file (mode 640, root:" + agentGroup + ")")
		fmt.Println("          then: sudo systemctl restart rt-node-agent")
	}
	return nil
}

// ensureTokenLinux writes a fresh token to path if missing. Returns the
// new token (or "" if one was already present). Always re-applies correct
// ownership and perms so a pre-existing mis-chmodded file gets healed on
// reinstall — that was the v0.1.1 foot-gun.
func ensureTokenLinux(path string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		// Heal perms on an existing token file without rotating the secret.
		_ = run("chown", "root:"+agentGroup, path)
		_ = os.Chmod(path, 0o640)
		return "", nil
	}
	tok, err := generateToken()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o640); err != nil {
		return "", err
	}
	if err := run("chown", "root:"+agentGroup, path); err != nil {
		return "", err
	}
	return tok, nil
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
