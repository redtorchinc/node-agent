package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// configExampleTemplate is dropped into the config directory on first install
// so operators can `cp config.yaml.example config.yaml` and edit instead of
// hunting through the repo for the example.
//
// Kept in source rather than go:embed so a single file change stays obvious
// in PRs; sync with examples/config.yaml when this changes.
const configExampleTemplate = `# rt-node-agent config — copy to config.yaml and edit.
# Every field is optional — defaults match SPEC.md §HTTP API.

# HTTP listener.
# port: 11435
# bind: 0.0.0.0

# Bearer token for POST /actions/*. The installer generates one at
# /etc/rt-node-agent/token (Linux/macOS) or %ProgramData%\rt-node-agent\token
# (Windows) automatically. To use a custom token, write it to that path with
# the right perms (640 root:rt-agent on Linux, 600 on macOS) and restart.
# Uncomment to override via this file instead of the token file:
# token: "inline-only-for-dev"
# token_file: /etc/rt-node-agent/token

# Local Ollama endpoint. Override if Ollama listens on a non-default host.
# ollama_endpoint: http://localhost:11434

# Expose /metrics in Prometheus text format. Off by default.
# metrics_enabled: false

# Optional: scrape cooperating services that expose PyTorch allocator JSON.
# See SPEC.md §"Service allocator scraping" for the contract. Both thresholds
# must be exceeded for the corresponding degraded reason to fire:
#   reserved/allocated > 3.0 AND reserved_mb > threshold_critical_mb → vram_service_creep_critical
#   reserved/allocated > 2.0 AND reserved_mb > threshold_warn_mb     → vram_service_creep_warn
# service_allocators:
#   - name: gliner2-service
#     url: http://localhost:8077/v1/debug/gpu
#     threshold_warn_mb: 4096
#     threshold_critical_mb: 10240
#     scrape_interval_s: 30
`

// writeConfigExample drops config.yaml.example into dir if missing. Idempotent —
// never overwrites, so operator edits to an existing example survive reinstall.
// The real config.yaml is left untouched; this is a template only.
func writeConfigExample(dir string) error {
	p := filepath.Join(dir, "config.yaml.example")
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return os.WriteFile(p, []byte(configExampleTemplate), 0o644)
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
