package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/redtorchinc/node-agent/internal/services"
)

// serviceReq is the wire shape for POST /actions/service.
//
// Deliberately tiny: only `unit` (must match the allowlist verbatim) and
// `action` (closed enum). No environment, no override args, no command
// substitution paths.
type serviceReq struct {
	Unit   string `json:"unit"`
	Action string `json:"action"`
}

type serviceResp struct {
	Status string          `json:"status"`
	services.Result `json:",inline"`
}

// handleServiceAction is wrapped with requireToken so a missing/invalid
// Bearer 401s before we even look at the body.
func (s *Server) handleServiceAction(w http.ResponseWriter, r *http.Request) {
	if s.svcMgr == nil {
		http.Error(w, "service control not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req serviceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	req.Unit = strings.TrimSpace(req.Unit)
	req.Action = strings.TrimSpace(req.Action)
	if req.Unit == "" {
		http.Error(w, "missing unit", http.StatusBadRequest)
		return
	}
	if req.Action == "" {
		http.Error(w, "missing action", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	res, err := s.svcMgr.Do(ctx, req.Unit, services.Action(req.Action))
	if err != nil {
		code, msg := mapServiceErr(err)
		slog.Warn("service action denied or failed",
			"unit", req.Unit, "action", req.Action, "code", code, "err", err, "remote", r.RemoteAddr)
		http.Error(w, msg, code)
		return
	}
	slog.Info("service action ok",
		"unit", req.Unit, "action", req.Action,
		"active", res.ActiveState, "sub", res.SubState,
		"took_ms", res.TookMS, "remote", r.RemoteAddr)
	writeJSON(w, http.StatusOK, serviceResp{Status: "ok", Result: res})
}

// mapServiceErr turns typed errors from internal/services into HTTP codes.
// See V0_2_0_PLAN.md §A3 for the contract.
func mapServiceErr(err error) (int, string) {
	switch {
	case errors.Is(err, services.ErrUnitNotAllowed):
		return http.StatusForbidden, "unit not in allowlist"
	case errors.Is(err, services.ErrActionNotAllowed):
		return http.StatusConflict, "action not permitted for this unit"
	case errors.Is(err, services.ErrUnknownAction):
		return http.StatusBadRequest, "unknown action (start|stop|restart|status)"
	case errors.Is(err, services.ErrUnitNotFound):
		return http.StatusNotFound, "unit not known to systemd"
	case errors.Is(err, services.ErrUnsupported):
		return http.StatusNotImplemented, "service control not supported on this OS"
	default:
		return http.StatusInternalServerError, err.Error()
	}
}
