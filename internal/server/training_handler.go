package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/redtorchinc/node-agent/internal/mode"
)

// trainingReq is the body for POST /actions/training-mode.
type trainingReq struct {
	Enter                bool     `json:"enter"`
	RunID                string   `json:"run_id"`
	ExpectedDurationS    int64    `json:"expected_duration_s"`
	ReleaseOllamaModels  []string `json:"release_ollama_models"`
	RestoreOnExit        *bool    `json:"restore_on_exit"`
}

type trainingResp struct {
	Status          string   `json:"status"`
	Mode            string   `json:"mode"`
	RunID           string   `json:"run_id,omitempty"`
	PreviousRunID   string   `json:"previous_run_id,omitempty"`
	EnteredAt       int64    `json:"entered_at,omitempty"`
	DurationS       int64    `json:"duration_s,omitempty"`
	ModelsReleased  []string `json:"models_released,omitempty"`
	TookMS          int64    `json:"took_ms"`
}

func (s *Server) handleTrainingMode(w http.ResponseWriter, r *http.Request) {
	if s.modeMgr == nil {
		http.Error(w, "training-mode not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req trainingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	start := time.Now()
	if !req.Enter {
		cleared, prev, dur := s.modeMgr.Exit()
		if !cleared {
			http.Error(w, "not in training_mode", http.StatusConflict)
			return
		}
		slog.Info("training-mode exit ok",
			"previous_run_id", prev, "duration_s", dur, "remote", r.RemoteAddr)
		writeJSON(w, http.StatusOK, trainingResp{
			Status:        "ok",
			Mode:          "idle",
			PreviousRunID: prev,
			DurationS:     dur,
			TookMS:        time.Since(start).Milliseconds(),
		})
		return
	}

	if req.RunID == "" {
		http.Error(w, "run_id required for enter", http.StatusBadRequest)
		return
	}

	// Drain ollama BEFORE recording training-mode entry, so a failed unload
	// doesn't leave the node in training_mode with stale models still loaded.
	released := []string{}
	for _, m := range req.ReleaseOllamaModels {
		if m == "" {
			continue
		}
		if _, err := s.ollama.Unload(ctx, m); err != nil {
			slog.Warn("training-mode entry blocked by unload failure",
				"model", m, "err", err, "remote", r.RemoteAddr)
			http.Error(w, "failed to unload "+m+": "+err.Error(), http.StatusInternalServerError)
			return
		}
		released = append(released, m)
	}

	restore := true
	if req.RestoreOnExit != nil {
		restore = *req.RestoreOnExit
	}
	if err := s.modeMgr.Enter(mode.EnterRequest{
		RunID:               req.RunID,
		ExpectedDurationS:   req.ExpectedDurationS,
		ReleaseOllamaModels: released,
		RestoreOnExit:       restore,
	}); err != nil {
		// Conflict (already in training_mode with different run_id)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	slog.Info("training-mode entered",
		"run_id", req.RunID,
		"released", released,
		"expected_s", req.ExpectedDurationS,
		"remote", r.RemoteAddr)
	writeJSON(w, http.StatusOK, trainingResp{
		Status:         "ok",
		Mode:           mode.ModeTraining,
		RunID:          req.RunID,
		EnteredAt:      time.Now().Unix(),
		ModelsReleased: released,
		TookMS:         time.Since(start).Milliseconds(),
	})
}
