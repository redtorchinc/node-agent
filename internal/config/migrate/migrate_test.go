package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const v1Existing = `# operator notes: this is the prod inference box
port: 11435
bind: 0.0.0.0
token_file: /etc/rt-node-agent/token
ollama_endpoint: http://localhost:11434
metrics_enabled: false

service_allocators:
  - name: gliner2-service
    url: http://localhost:8077/v1/debug/gpu
    threshold_warn_mb: 4096
    threshold_critical_mb: 10240
    scrape_interval_s: 30
`

const v2Default = `config_version: 2
port: 11435
bind: 0.0.0.0
token_file: /etc/rt-node-agent/token
metrics_enabled: false
ollama_endpoint: http://localhost:11434

platforms:
  ollama:
    enabled: auto
    endpoint: http://localhost:11434
  vllm:
    enabled: auto
    endpoint: http://localhost:8000

services:
  manager: systemd
  allowed: []

service_allocators: []

training_mode:
  state_file: /var/lib/rt-node-agent/training_mode.json
  grace_period_s: 3600
`

func TestMigrate_v1ToV2_AppendsMissingKeys(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(v1Existing), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Migrate(cfg, v2Default)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if res.AlreadyCurrent {
		t.Fatalf("expected migration, got AlreadyCurrent")
	}
	if res.NewConfigPath != cfg+".new" {
		t.Fatalf("unexpected NewConfigPath: %s", res.NewConfigPath)
	}
	if res.OldVersion != 0 {
		t.Fatalf("expected OldVersion=0 (absent), got %d", res.OldVersion)
	}
	if res.NewVersion != 2 {
		t.Fatalf("expected NewVersion=2, got %d", res.NewVersion)
	}

	// platforms, services, training_mode should all be appended.
	wantKeys := []string{"platforms", "services", "training_mode"}
	for _, k := range wantKeys {
		found := false
		for _, got := range res.AddedKeys {
			if got == k {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q in AddedKeys, got %v", k, res.AddedKeys)
		}
	}

	out, err := os.ReadFile(res.NewConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	// Original content preserved verbatim — including the operator comment.
	if !strings.Contains(s, "# operator notes: this is the prod inference box") {
		t.Errorf("original operator comment not preserved")
	}
	if !strings.Contains(s, "url: http://localhost:8077/v1/debug/gpu") {
		t.Errorf("original allocator URL not preserved")
	}

	// New additions must be fully commented out — no live YAML keys.
	// Slice from the start of the section header line, not from the literal
	// "v0.2.0 additions" text mid-line.
	hdrIdx := strings.Index(s, "# ")
	if i := strings.Index(s, "v0.2.0 additions"); i >= 0 {
		// Back up to the start of the line containing the header.
		hdrIdx = strings.LastIndex(s[:i], "\n") + 1
	}
	addedSection := s[hdrIdx:]
	for _, line := range strings.Split(addedSection, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "#") {
			t.Errorf("uncommented line in additions section: %q", line)
		}
	}

	// Banner is non-empty and includes both file paths.
	banner := res.Banner(cfg)
	if banner == "" {
		t.Fatalf("expected non-empty banner")
	}
	if !strings.Contains(banner, cfg) || !strings.Contains(banner, res.NewConfigPath) {
		t.Errorf("banner missing paths: %s", banner)
	}
}

func TestMigrate_AlreadyCurrent_NoOp(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	// A v2 config with all default top-level keys present.
	if err := os.WriteFile(cfg, []byte(v2Default), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Migrate(cfg, v2Default)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !res.AlreadyCurrent {
		t.Errorf("expected AlreadyCurrent=true on matching config")
	}
	if res.NewConfigPath != "" {
		t.Errorf("expected no .new file, got %s", res.NewConfigPath)
	}
	if _, err := os.Stat(cfg + ".new"); !os.IsNotExist(err) {
		t.Errorf(".new file should not exist when no migration needed")
	}
}

func TestMigrate_MissingExistingFile_ReturnsAlreadyCurrent(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "does-not-exist.yaml")
	res, err := Migrate(cfg, v2Default)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !res.AlreadyCurrent {
		t.Errorf("expected AlreadyCurrent=true when source file is absent")
	}
}

func TestMigrate_PreservesExistingValues(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	customized := `port: 19999
bind: 127.0.0.1
ollama_endpoint: http://192.168.0.10:11434
`
	if err := os.WriteFile(cfg, []byte(customized), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Migrate(cfg, v2Default)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	out, _ := os.ReadFile(res.NewConfigPath)
	s := string(out)
	if !strings.Contains(s, "port: 19999") {
		t.Errorf("custom port lost")
	}
	if !strings.Contains(s, "192.168.0.10") {
		t.Errorf("custom ollama_endpoint lost")
	}
}
