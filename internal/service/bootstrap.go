package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/redtorchinc/node-agent/internal/config"
	"github.com/redtorchinc/node-agent/internal/config/migrate"
)

// writeConfigExample drops config.yaml.example into dir. Always overwrites,
// because the example must match the current binary's schema — operators
// rely on it as a reference when comparing a `.new` migration output. The
// REAL config (config.yaml without the .example suffix) is untouched.
//
// Sources from config.DefaultYAML so example, migrate reference, and
// in-memory defaults all agree.
func writeConfigExample(dir string) error {
	p := filepath.Join(dir, "config.yaml.example")
	return os.WriteFile(p, []byte(config.DefaultYAML), 0o644)
}

// runConfigMigrate is called from the OS-specific install() right after
// the agent user/dirs are in place. Two paths:
//
//  1. Normal upgrade — existing config.yaml parses cleanly but is missing
//     v0.2.0 keys. Migrate writes config.yaml.new with the keys appended
//     (commented). Operator reviews and `mv`s when ready.
//  2. Broken existing config.yaml (YAML syntax error from a hand-edit gone
//     wrong, or v0.1 file that never worked). Migrate returns ErrBrokenYAML;
//     we automatically back up the broken file to config.yaml.broken-<ts>
//     and write a fresh default config.yaml so the service can start. The
//     operator's content isn't destroyed; just moved aside.
//
// Token file is NEVER touched.
func runConfigMigrate(configPath string) {
	res, err := migrate.Migrate(configPath, config.DefaultYAML)
	if err == nil {
		if banner := res.Banner(configPath); banner != "" {
			fmt.Print(banner)
		}
		return
	}
	if errors.Is(err, migrate.ErrBrokenYAML) {
		backup, ferr := migrate.ForceReset(configPath, config.DefaultYAML)
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "config migrate-force failed: %v\n", ferr)
			return
		}
		fmt.Print(brokenYAMLBanner(configPath, backup, err))
		return
	}
	// Other I/O errors (permissions, disk full): surface them but don't
	// abort the install — the service may still come up with defaults.
	fmt.Fprintf(os.Stderr, "config migrate: %v\n", err)
}

func brokenYAMLBanner(configPath, backup string, cause error) string {
	return fmt.Sprintf(`
*** rt-node-agent: existing config.yaml could not be parsed — auto-recovered ***
  cause:   %v
  backup:  %s
  current: %s (now contains v0.2.0 defaults; edit and restart to apply changes)

  diff your old config:  diff %s %s
  restart after editing: %s

The token file at %s was not touched.

`, cause, backup, configPath, backup, configPath, restartCommandForOS(), tokenPathForOS())
}

// restartCommandForOS returns the OS-appropriate restart command for
// operator banners. Mirrors the migrate package's helper but lives here
// so internal/service doesn't import internal/config/migrate just for a
// string.
func restartCommandForOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "sudo launchctl kickstart -k system/com.redtorch.rt-node-agent"
	case "windows":
		return "Restart-Service rt-node-agent  (elevated PowerShell)"
	default:
		return "sudo systemctl restart rt-node-agent"
	}
}

// tokenPathForOS returns the OS-default token-file path. Linux and macOS
// share /etc/rt-node-agent/token; Windows uses %ProgramData%.
func tokenPathForOS() string {
	if runtime.GOOS == "windows" {
		return `%ProgramData%\rt-node-agent\token`
	}
	return "/etc/rt-node-agent/token"
}

// generateToken returns a 64-char hex-encoded 32-byte random token — the same
// entropy as `openssl rand -hex 32`. Errors only if crypto/rand is broken.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
