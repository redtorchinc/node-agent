//go:build darwin

package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

	// Order matters here. A prior version of the agent may already be
	// bootstrapped under our label, AND launchctl is strict about plist
	// ownership (must be root:wheel mode 0644) AND about not seeing the
	// on-disk plist mutate mid-bootstrap. The sequence that survives all
	// of those:
	//
	//   1. bootout the OLD service while the OLD plist is still on disk
	//      — the kernel reads the plist to identify what to tear down.
	//      Overwriting first leaves it half-bootstrapped → EIO.
	//   2. (optionally) wait briefly for the daemon to exit; on a slow
	//      box the bootstrap can race the bootout teardown.
	//   3. Write the new plist using /usr/bin/install with -o root -g
	//      wheel -m 644 so we get the exact ownership launchctl wants.
	//      os.WriteFile would inherit the parent shell's group (often
	//      staff under sudo), which silently makes bootstrap fail with
	//      EIO on some macOS releases.
	//   4. Whitelist the binary with the Application Firewall.
	//   5. Bootstrap.
	//
	// If bootout fails because no prior install exists, that's expected
	// on a fresh box — only surface bootout errors that come back with
	// something other than "service not found" (exit 113).
	allowFirewall(exe)
	bootoutExisting(launchdLabel, launchdPlist)

	if err := writeRootWheelFile(launchdPlist, plistTemplate, launchdLabel, exe, logMacOut, logMacErr); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	// Bootstrap loads + starts in one step on modern macOS. If it still
	// fails after the defensive bootout, surface a manual-recovery hint
	// pointing at the canonical sequence so the operator isn't guessing.
	if err := runLaunchctl("bootstrap", "system", launchdPlist); err != nil {
		return fmt.Errorf(`launchctl bootstrap: %w

A previous load is likely wedged. Recover by hand:
  sudo launchctl bootout system/%s 2>/dev/null
  sudo rm -f %s
  sudo pkill -f %s
  sudo rt-node-agent install`, err, launchdLabel, launchdPlist, exe)
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

// runLaunchctlQuiet runs launchctl with stderr discarded. Used for
// defensive cleanup calls (bootout-before-bootstrap) where a "service
// not found" failure is expected on a fresh install and would otherwise
// clutter the install output.
func runLaunchctlQuiet(args ...string) error {
	return exec.Command("launchctl", args...).Run()
}

// bootoutExisting tears down a prior install. Two passes: the modern
// label form, then the legacy plist-path form, because a service
// bootstrapped on an older macOS may not be addressable by label.
// Stderr is captured and only surfaced if it doesn't match the
// "service not found" case (exit 113), which is expected on a fresh
// install. After bootout, wait up to 2s for the daemon process to
// exit — racing the teardown causes bootstrap to fail with EIO.
func bootoutExisting(label, plistPath string) {
	for _, args := range [][]string{
		{"bootout", "system/" + label},
		{"bootout", "system", plistPath},
	} {
		cmd := exec.Command("launchctl", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err == nil {
			break
		}
		// Exit code 113 (or its string form) = "Could not find specified
		// service" — fine, just nothing to remove.
		msg := stderr.String()
		if strings.Contains(msg, "Could not find") || strings.Contains(msg, "113") {
			continue
		}
		// Anything else is worth showing the operator (won't abort
		// install — the subsequent bootstrap might still succeed).
		fmt.Fprintf(os.Stderr, "launchctl %s: %v: %s\n", strings.Join(args, " "), err, strings.TrimSpace(msg))
	}
	// Best-effort settle. macOS sometimes returns from bootout before
	// the process has fully exited; an immediate bootstrap then races
	// the teardown. 250ms × 8 = 2s max is small relative to install
	// time and saves a wedged daemon.
	for i := 0; i < 8; i++ {
		if !labelLoaded(label) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// labelLoaded reports whether `launchctl print system/<label>` succeeds
// (meaning the kernel still has the service bootstrapped). Used to wait
// out the teardown between bootout and bootstrap.
func labelLoaded(label string) bool {
	return exec.Command("launchctl", "print", "system/"+label).Run() == nil
}

// writeRootWheelFile renders the plist and writes it with explicit
// root:wheel ownership and mode 0644. /usr/bin/install handles the
// chown+chmod in one syscall; falling back to os.WriteFile + chown
// would race a launchctl tail that's already watching the path.
func writeRootWheelFile(dst, tmpl string, args ...interface{}) error {
	content := fmt.Sprintf(tmpl, args...)
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".plist.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// /usr/bin/install -o root -g wheel -m 644 src dst — atomic rename
	// into place with the exact ownership launchctl requires.
	cmd := exec.Command("/usr/bin/install", "-o", "root", "-g", "wheel", "-m", "644", tmpName, dst)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
