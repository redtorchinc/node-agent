//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	launchdPlist = "/Library/LaunchDaemons/com.redtorch.rt-node-agent.plist"
	launchdLabel = "com.redtorch.rt-node-agent"
	configDirMac = "/etc/rt-node-agent"
	logMacOut    = "/var/log/rt-node-agent.log"
	logMacErr    = "/var/log/rt-node-agent.err"
	agentUserMac = "_rt_agent"
)

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>          <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>run</string>
  </array>
  <key>RunAtLoad</key>      <true/>
  <key>KeepAlive</key>      <true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`

// install writes the plist and bootstraps the daemon. macOS system daemons
// must live under /Library/LaunchDaemons and must be loaded as root.
func install() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("install requires root (try: sudo rt-node-agent install)")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)

	if err := os.MkdirAll(configDirMac, 0o755); err != nil {
		return err
	}
	if err := writeConfigExample(configDirMac); err != nil {
		return fmt.Errorf("write config.yaml.example: %w", err)
	}

	// Migrate any existing config to the v0.2.0 schema (writes .new only,
	// never mutates the operator's config in place).
	runConfigMigrate(filepath.Join(configDirMac, "config.yaml"))

	// Token bootstrap. macOS daemon runs as root (no UserName in the plist),
	// so 0600 root-only is the correct perm — no group-read needed.
	tokenPath := filepath.Join(configDirMac, "token")
	newToken, err := ensureTokenMac(tokenPath)
	if err != nil {
		return fmt.Errorf("bootstrap token: %w", err)
	}

	plist := fmt.Sprintf(plistTemplate, launchdLabel, exe, logMacOut, logMacErr)
	if err := os.WriteFile(launchdPlist, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	// Whitelist with the macOS Application Firewall. If the firewall is off,
	// these calls are silent no-ops; if on, they stop it from dropping
	// incoming connections to the agent's port. Errors are logged but not
	// fatal — a misconfigured firewall shouldn't block install on a box
	// where the firewall isn't in use.
	allowFirewall(exe)

	// Bootstrap loads + starts in one step on modern macOS.
	if err := runLaunchctl("bootstrap", "system", launchdPlist); err != nil {
		return err
	}
	_ = runLaunchctl("enable", "system/"+launchdLabel)
	fmt.Println("rt-node-agent installed and started (launchd)")
	if newToken != "" {
		fmt.Println()
		fmt.Println("A bearer token was generated and written to " + tokenPath + ":")
		fmt.Println("  " + newToken)
		fmt.Println()
		fmt.Println("The case-manager backend will use this token for POST /actions/*.")
		fmt.Println("To rotate: write a new value to the file (mode 600, owner root)")
		fmt.Println("          then: sudo launchctl kickstart -k system/" + launchdLabel)
	}
	return nil
}

// ensureTokenMac writes a fresh token at path if missing. Returns the
// generated token, or "" when one was already in place. Heals perms on
// reinstall so a manually-created 644 file doesn't leak.
func ensureTokenMac(path string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		_ = os.Chmod(path, 0o600)
		return "", nil
	}
	tok, err := generateToken()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// allowFirewall adds the binary to the macOS Application Firewall allow-list
// and unblocks incoming connections. socketfilterfw lives at a stable path
// across every supported macOS version.
func allowFirewall(exe string) {
	const fw = "/usr/libexec/ApplicationFirewall/socketfilterfw"
	if _, err := os.Stat(fw); err != nil {
		return
	}
	cmd := exec.Command(fw, "--add", exe)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
	cmd = exec.Command(fw, "--unblockapp", exe)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

func uninstall() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("uninstall requires root")
	}
	_ = runLaunchctl("bootout", "system/"+launchdLabel)
	if err := os.Remove(launchdPlist); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Firewall rule is keyed on the binary path; remove it so a future
	// install to the same path doesn't inherit a stale entry.
	const fw = "/usr/libexec/ApplicationFirewall/socketfilterfw"
	if _, err := os.Stat(fw); err == nil {
		if exe, err := os.Executable(); err == nil {
			exe, _ = filepath.EvalSymlinks(exe)
			_ = exec.Command(fw, "--remove", exe).Run()
		}
	}
	fmt.Println("rt-node-agent uninstalled (config preserved at " + configDirMac + ")")
	return nil
}

func startSvc() error { return runLaunchctl("kickstart", "system/"+launchdLabel) }
func stopSvc() error  { return runLaunchctl("stop", "system/"+launchdLabel) }

func status() (State, error) {
	if _, err := os.Stat(launchdPlist); os.IsNotExist(err) {
		return StateNotInstalled, nil
	}
	out, err := exec.Command("launchctl", "list").CombinedOutput()
	if err != nil {
		return StateUnknown, nil
	}
	if strings.Contains(string(out), launchdLabel) {
		return StateRunning, nil
	}
	return StateStopped, nil
}

func runLaunchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
