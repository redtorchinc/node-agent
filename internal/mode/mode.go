// Package mode is the training-mode state machine. The wire contract
// matches NODE_AGENT_TRAINING_EXTENSIONS.md §4 / §7.
//
// State machine:
//
//	  idle ─┬─> inference (implicit — derived from platforms[].models)
//	        │
//	        └─> training_mode (explicit — POST /actions/training-mode)
//	training_mode ──> idle (explicit exit OR expected_duration + grace expired)
//
// Persistence: when training_mode is active, the agent writes a single
// JSON state file (default /var/lib/rt-node-agent/training_mode.json) so
// a crash mid-training doesn't leak the node back into inference. On
// startup the state file is read and, if the run window has expired
// (entered_at + expected + grace), auto-cleared with a warning log.
//
// Concurrency: a single sync.Mutex guards all state. Reads (Get, Mode,
// Training) take RLock; transitions take Lock. The state file write is
// best-effort — a write failure logs but does not block the transition.
package mode

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/redtorchinc/node-agent/internal/health"
)

const (
	ModeIdle      = "idle"
	ModeInference = "inference"
	ModeTraining  = "training_mode"
)

// Snapshot is the persisted training-mode record. Lives on disk; the
// in-memory copy is mirrored from this so a crash doesn't desync.
type Snapshot struct {
	RunID                 string   `json:"run_id"`
	EnteredAt             int64    `json:"entered_at"`
	ExpectedDurationS     int64    `json:"expected_duration_s,omitempty"`
	OllamaModelsReleased  []string `json:"ollama_models_released"`
	OllamaModelsToRestore []string `json:"ollama_models_to_restore"`
}

// Manager owns the state machine. Construct with New(); the typical flow is:
//
//	m := mode.New(cfg)
//	m.Restore()              // on startup, before serving
//	mode.SetReporter(m)      // wire into the health reporter
//
//	... later, on POST /actions/training-mode:
//	m.Enter(req)
//	m.Exit()
type Manager struct {
	stateFile    string
	gracePeriodS int64

	mu       sync.RWMutex
	training *Snapshot // nil = not in training_mode
}

// New returns a Manager wired to the configured state-file path.
func New(stateFile string, gracePeriodS int64) *Manager {
	if gracePeriodS <= 0 {
		gracePeriodS = 3600
	}
	return &Manager{
		stateFile:    stateFile,
		gracePeriodS: gracePeriodS,
	}
}

// Restore reads the state file on startup. If the run window has expired
// (entered_at + expected + grace), the state file is cleared and a warning
// logged — auto-recovery for crashes during long training runs.
func (m *Manager) Restore() {
	b, err := os.ReadFile(m.stateFile)
	if err != nil {
		return
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		slog.Warn("training-mode state file is corrupt; clearing",
			"path", m.stateFile, "err", err)
		_ = os.Remove(m.stateFile)
		return
	}
	if s.RunID == "" || s.EnteredAt == 0 {
		_ = os.Remove(m.stateFile)
		return
	}
	now := time.Now().Unix()
	deadline := s.EnteredAt + s.ExpectedDurationS + m.gracePeriodS
	if s.ExpectedDurationS > 0 && now > deadline {
		slog.Warn("training-mode state expired during downtime — auto-exiting",
			"run_id", s.RunID, "entered_at", s.EnteredAt,
			"expected_s", s.ExpectedDurationS, "grace_s", m.gracePeriodS)
		_ = os.Remove(m.stateFile)
		return
	}
	m.mu.Lock()
	m.training = &s
	m.mu.Unlock()
	slog.Info("training-mode restored from state file",
		"run_id", s.RunID, "entered_at", s.EnteredAt)
}

// Mode returns the current mode string. Always "" / "idle" / "inference"
// from this layer; "inference" is computed by the reporter (it needs to
// look at platforms[].models), so this returns "" when not in training_mode
// and the reporter picks the default.
func (m *Manager) Mode() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.training != nil {
		return ModeTraining
	}
	return ""
}

// Training returns the current training snapshot or nil when idle/inference.
// Returns a *health.TrainingState so health doesn't need to import mode.
func (m *Manager) Training() *health.TrainingState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.training == nil {
		return nil
	}
	return &health.TrainingState{
		RunID:                 m.training.RunID,
		EnteredAt:             m.training.EnteredAt,
		ExpectedDurationS:     m.training.ExpectedDurationS,
		OllamaModelsReleased:  append([]string(nil), m.training.OllamaModelsReleased...),
		OllamaModelsToRestore: append([]string(nil), m.training.OllamaModelsToRestore...),
	}
}

// EnterRequest is the body of POST /actions/training-mode {enter: true, ...}.
type EnterRequest struct {
	RunID                string
	ExpectedDurationS    int64
	ReleaseOllamaModels  []string
	RestoreOnExit        bool
}

// Enter records the training-mode entry and persists the state file. The
// caller is responsible for draining ollama (call Enter only after all
// requested unloads succeed).
//
// Returns an error if already in training_mode AND the run_id differs —
// idempotent re-entry with the same run_id is a no-op (returns nil so the
// caller can return 200).
func (m *Manager) Enter(req EnterRequest) error {
	if req.RunID == "" {
		return fmt.Errorf("run_id required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.training != nil {
		if m.training.RunID == req.RunID {
			return nil // idempotent
		}
		return fmt.Errorf("already in training_mode with run_id=%s", m.training.RunID)
	}
	s := &Snapshot{
		RunID:                 req.RunID,
		EnteredAt:             time.Now().Unix(),
		ExpectedDurationS:     req.ExpectedDurationS,
		OllamaModelsReleased:  append([]string(nil), req.ReleaseOllamaModels...),
	}
	if req.RestoreOnExit {
		s.OllamaModelsToRestore = append([]string(nil), req.ReleaseOllamaModels...)
	} else {
		s.OllamaModelsToRestore = []string{}
	}
	m.training = s
	m.writeStateLocked()
	return nil
}

// Exit clears training_mode. Returns false if not currently in training_mode
// (so the HTTP handler can return 409).
func (m *Manager) Exit() (cleared bool, previousRunID string, duration int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.training == nil {
		return false, "", 0
	}
	previousRunID = m.training.RunID
	duration = time.Now().Unix() - m.training.EnteredAt
	m.training = nil
	_ = os.Remove(m.stateFile)
	return true, previousRunID, duration
}

// InTraining is a cheap check for the allocator-scraper "only_when_mode" hook.
func (m *Manager) InTraining() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.training != nil
}

// writeStateLocked persists the current training snapshot. Caller must
// hold m.mu.Lock(). Atomic via temp+rename so a crash mid-write never
// leaves a half-written file.
func (m *Manager) writeStateLocked() {
	if m.stateFile == "" || m.training == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.stateFile), 0o755); err != nil {
		slog.Warn("training-mode state dir mkdir failed", "path", m.stateFile, "err", err)
	}
	b, err := json.MarshalIndent(m.training, "", "  ")
	if err != nil {
		slog.Warn("training-mode state marshal failed", "err", err)
		return
	}
	tmp := m.stateFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		slog.Warn("training-mode state write failed", "path", tmp, "err", err)
		return
	}
	if err := os.Rename(tmp, m.stateFile); err != nil {
		slog.Warn("training-mode state rename failed", "path", m.stateFile, "err", err)
	}
}
