package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/redtorchinc/node-agent/internal/config"
	"github.com/redtorchinc/node-agent/internal/config/migrate"
)

// writeConfigExample drops config.yaml.example into dir if missing. Idempotent —
// never overwrites, so operator edits to an existing example survive reinstall.
// The real config.yaml is left untouched; this is a template only.
//
// Sources the content from config.DefaultYAML so the example, the migrate
// reference, and the in-memory defaults all agree.
func writeConfigExample(dir string) error {
	p := filepath.Join(dir, "config.yaml.example")
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return os.WriteFile(p, []byte(config.DefaultYAML), 0o644)
}

// runConfigMigrate is called from the OS-specific install() right after the
// agent user/dirs are in place. If /etc/rt-node-agent/config.yaml exists
// and is older than the current schema (or has missing top-level keys),
// it writes /etc/rt-node-agent/config.yaml.new with the missing keys
// commented in, and prints a banner pointing the operator at the diff.
//
// Never overwrites the existing config — the merge requires operator review.
// Token file is untouched.
func runConfigMigrate(configPath string) {
	res, err := migrate.Migrate(configPath, config.DefaultYAML)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config migrate: %v\n", err)
		return
	}
	if banner := res.Banner(configPath); banner != "" {
		fmt.Print(banner)
	}
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
