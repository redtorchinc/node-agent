package migrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const v1Existing = `# operator notes: this is the prod inference box
port: 19999
bind: 127.0.0.1
token_file: /etc/rt-node-agent/token
ollama_endpoint: http://192.168.0.10:11434
metrics_enabled: true

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

// TestMigrate_v1ToV2_MergesInPlace is the headline test for v0.2.7.
// Old behaviour: append commented additions to a `.new` sidecar; each
// re-install added another duplicate block. New behaviour: back up to
// `.bak`, write fresh defaults to the live path with operator's values
// grafted in. No `.new` sidecar, no duplicate blocks.
func TestMigrate_v1ToV2_MergesInPlace(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(v1Existing), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Migrate(cfg, v2Default)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if res.Action != ActionMerged {
		t.Fatalf("Action = %q, want %q", res.Action, ActionMerged)
	}

	// .bak must exist and contain the original verbatim.
	if res.BackupPath != cfg+".bak" {
		t.Fatalf("BackupPath = %q, want %q", res.BackupPath, cfg+".bak")
	}
	bakBytes, err := os.ReadFile(res.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(bakBytes) != v1Existing {
		t.Errorf(".bak content differs from original; operator can't recover from this")
	}

	// .new sidecar must NOT exist any more.
	if _, err := os.Stat(cfg + ".new"); !os.IsNotExist(err) {
		t.Errorf(".new sidecar should not be created; got stat err=%v", err)
	}

	// Live config exists, parses as YAML, has the new schema version
	// and operator's customised values.
	liveBytes, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	live := string(liveBytes)
	if !strings.Contains(live, "config_version: 2") {
		t.Errorf("merged file missing config_version: %s", live)
	}
	if !strings.Contains(live, "port: 19999") {
		t.Errorf("merged file lost operator's port: %s", live)
	}
	if !strings.Contains(live, "192.168.0.10") {
		t.Errorf("merged file lost operator's ollama_endpoint: %s", live)
	}
	if !strings.Contains(live, "metrics_enabled: true") {
		t.Errorf("merged file lost operator's metrics_enabled: %s", live)
	}

	// New features default to their schema defaults.
	if !strings.Contains(live, "platforms:") {
		t.Errorf("merged file missing platforms section")
	}
	if !strings.Contains(live, "vllm:") {
		t.Errorf("merged file missing vllm subsection")
	}

	// PreservedKeys lists what was grafted.
	wantPreserved := map[string]bool{
		"port": true, "bind": true, "token_file": true,
		"ollama_endpoint": true, "metrics_enabled": true,
		"service_allocators": true,
	}
	for _, k := range res.PreservedKeys {
		delete(wantPreserved, k)
	}
	if len(wantPreserved) > 0 {
		t.Errorf("expected these keys in PreservedKeys but missing: %v", wantPreserved)
	}
}

// TestMigrate_Idempotent_NoDuplicateBlocks is the regression test for
// the bug that prompted this redesign: running Migrate twice in a row
// against the same live config must not create duplicate blocks.
func TestMigrate_Idempotent_NoDuplicateBlocks(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(v1Existing), 0o644); err != nil {
		t.Fatal(err)
	}
	// First pass migrates.
	if _, err := Migrate(cfg, v2Default); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	contentAfterFirst, _ := os.ReadFile(cfg)

	// Second pass against the same (now-v2) live file: should be a no-op.
	res, err := Migrate(cfg, v2Default)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if res.Action != ActionNoChange {
		t.Errorf("second migrate Action = %q, want %q (idempotency broken)", res.Action, ActionNoChange)
	}

	contentAfterSecond, _ := os.ReadFile(cfg)
	if string(contentAfterFirst) != string(contentAfterSecond) {
		t.Errorf("second migrate changed the live file; expected idempotent no-op\nbefore:\n%s\nafter:\n%s",
			contentAfterFirst, contentAfterSecond)
	}

	// "platforms:" should appear exactly once — no duplicate block.
	count := strings.Count(string(contentAfterSecond), "\nplatforms:")
	if count > 1 {
		t.Errorf("found %d occurrences of 'platforms:' in live config — duplicate block bug returned", count)
	}
}

func TestMigrate_AlreadyCurrent_NoOp(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(v2Default), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Migrate(cfg, v2Default)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if res.Action != ActionNoChange {
		t.Errorf("Action = %q, want %q", res.Action, ActionNoChange)
	}
	if res.BackupPath != "" {
		t.Errorf("BackupPath = %q, want empty for no-op", res.BackupPath)
	}
	// No .bak written.
	if _, err := os.Stat(cfg + ".bak"); !os.IsNotExist(err) {
		t.Errorf(".bak should not exist after no-op migration")
	}
}

func TestMigrate_MissingExistingFile_ReturnsFresh(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "does-not-exist.yaml")
	res, err := Migrate(cfg, v2Default)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if res.Action != ActionFresh {
		t.Errorf("Action = %q, want %q", res.Action, ActionFresh)
	}
	if !res.AlreadyCurrent() {
		t.Errorf("AlreadyCurrent should report true on Fresh action")
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Errorf("Migrate must not create config.yaml when none existed (install path writes the example instead)")
	}
}

func TestMigrate_BrokenYAML_ReturnsErrBrokenYAML(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("port: 11435\nbind: 0.0.0.0\nbroken value here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Migrate(cfg, v2Default)
	if err == nil {
		t.Fatalf("expected ErrBrokenYAML, got nil")
	}
	if !errors.Is(err, ErrBrokenYAML) {
		t.Errorf("expected errors.Is(err, ErrBrokenYAML)=true, got %v", err)
	}
	// The live file must not be touched when parse fails.
	got, _ := os.ReadFile(cfg)
	if !strings.Contains(string(got), "broken value here") {
		t.Errorf("Migrate must not mutate a broken file; it's the caller's job to ForceReset")
	}
}

func TestMigrate_DeprecatedKeysAreSurfaced(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	withDeprecated := v1Existing + "\nold_removed_setting: 42\n"
	if err := os.WriteFile(cfg, []byte(withDeprecated), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Migrate(cfg, v2Default)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if res.Action != ActionMerged {
		t.Fatalf("Action = %q, want %q", res.Action, ActionMerged)
	}
	found := false
	for _, k := range res.DroppedKeys {
		if k == "old_removed_setting" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("DroppedKeys = %v, expected to contain old_removed_setting", res.DroppedKeys)
	}
	// The deprecated key must NOT appear in the live merged file.
	live, _ := os.ReadFile(cfg)
	if strings.Contains(string(live), "old_removed_setting") {
		t.Errorf("merged file still contains deprecated key; should be dropped (recoverable from .bak)")
	}
	// It MUST appear in the .bak.
	bak, _ := os.ReadFile(res.BackupPath)
	if !strings.Contains(string(bak), "old_removed_setting") {
		t.Errorf(".bak should preserve the deprecated key for operator recovery")
	}
}

func TestMigrate_Banner_OnlyOnMerged(t *testing.T) {
	r := Result{Action: ActionNoChange}
	if r.Banner("/etc/cfg") != "" {
		t.Errorf("Banner should be empty on no-change")
	}
	r = Result{Action: ActionFresh}
	if r.Banner("/etc/cfg") != "" {
		t.Errorf("Banner should be empty on fresh install")
	}
	r = Result{
		Action:        ActionMerged,
		BackupPath:    "/etc/cfg.bak",
		PreservedKeys: []string{"port", "bind"},
		OldVersion:    1, NewVersion: 2,
	}
	b := r.Banner("/etc/cfg")
	if !strings.Contains(b, "/etc/cfg.bak") {
		t.Errorf("Banner missing backup path: %s", b)
	}
	if !strings.Contains(b, "port, bind") {
		t.Errorf("Banner missing preserved keys: %s", b)
	}
}

func TestForceReset_BacksUpExistingAndWritesDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	original := "port: 11435\nbroken yaml syntax\n"
	if err := os.WriteFile(cfg, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	backup, err := ForceReset(cfg, v2Default)
	if err != nil {
		t.Fatalf("ForceReset: %v", err)
	}
	if backup == "" {
		t.Fatalf("expected non-empty backup path")
	}
	if !strings.HasPrefix(backup, cfg+".broken-") {
		t.Errorf("backup path = %q, want %s.broken-<ts>", backup, cfg)
	}
	got, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("backup contents mismatch")
	}
	got, err = os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "config_version: 2") {
		t.Errorf("current file is not the v2 default: %s", string(got))
	}
}

func TestForceReset_NoExistingFile_WritesDefaultsOnly(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "missing.yaml")
	backup, err := ForceReset(cfg, v2Default)
	if err != nil {
		t.Fatalf("ForceReset: %v", err)
	}
	if backup != "" {
		t.Errorf("expected no backup, got %q", backup)
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Errorf("default file should exist after ForceReset, got %v", err)
	}
}
