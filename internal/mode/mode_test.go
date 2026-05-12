package mode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnter_Exit_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	sf := filepath.Join(dir, "training_mode.json")
	m := New(sf, 3600)

	if got := m.Mode(); got != "" {
		t.Errorf("fresh manager Mode()=%q, want empty", got)
	}

	if err := m.Enter(EnterRequest{RunID: "run-1", ExpectedDurationS: 60, ReleaseOllamaModels: []string{"m"}}); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if got := m.Mode(); got != ModeTraining {
		t.Errorf("Mode after Enter = %q, want %q", got, ModeTraining)
	}
	if !m.InTraining() {
		t.Errorf("InTraining()=false after Enter")
	}

	// State file present and parseable.
	if _, err := os.Stat(sf); err != nil {
		t.Fatalf("state file missing: %v", err)
	}
	b, _ := os.ReadFile(sf)
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("state file JSON: %v", err)
	}
	if s.RunID != "run-1" {
		t.Errorf("state file run_id=%q", s.RunID)
	}

	// Idempotent re-entry with same run_id.
	if err := m.Enter(EnterRequest{RunID: "run-1"}); err != nil {
		t.Errorf("idempotent re-enter should be nil-error, got %v", err)
	}
	// Conflict on different run_id.
	if err := m.Enter(EnterRequest{RunID: "run-2"}); err == nil {
		t.Errorf("expected conflict on different run_id")
	}

	cleared, prev, _ := m.Exit()
	if !cleared {
		t.Errorf("Exit should succeed when in training_mode")
	}
	if prev != "run-1" {
		t.Errorf("previous run_id=%q", prev)
	}

	if _, err := os.Stat(sf); !os.IsNotExist(err) {
		t.Errorf("state file should be removed after Exit, stat err=%v", err)
	}

	// Second Exit returns cleared=false.
	cleared, _, _ = m.Exit()
	if cleared {
		t.Errorf("Exit when idle should return cleared=false")
	}
}

func TestRestore_AutoExitWhenWindowExpired(t *testing.T) {
	dir := t.TempDir()
	sf := filepath.Join(dir, "training_mode.json")

	// Write a snapshot that's "started 2h ago, expected 60s, grace 3600s",
	// so deadline = -2h + 60s + 3600s ≈ -1h (already in the past).
	old := Snapshot{
		RunID:             "expired",
		EnteredAt:         time.Now().Add(-2 * time.Hour).Unix(),
		ExpectedDurationS: 60,
	}
	b, _ := json.Marshal(&old)
	if err := os.WriteFile(sf, b, 0o600); err != nil {
		t.Fatal(err)
	}

	m := New(sf, 3600)
	m.Restore()
	if m.InTraining() {
		t.Errorf("expired window should auto-clear; still in training")
	}
	if _, err := os.Stat(sf); !os.IsNotExist(err) {
		t.Errorf("expired state file should be removed")
	}
}

func TestRestore_KeepsWithinWindow(t *testing.T) {
	dir := t.TempDir()
	sf := filepath.Join(dir, "training_mode.json")

	s := Snapshot{
		RunID:             "still-running",
		EnteredAt:         time.Now().Unix() - 30,
		ExpectedDurationS: 7200, // 2h
	}
	b, _ := json.Marshal(&s)
	_ = os.WriteFile(sf, b, 0o600)

	m := New(sf, 3600)
	m.Restore()
	if !m.InTraining() {
		t.Errorf("recent training-mode should be restored")
	}
}
