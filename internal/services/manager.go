// Package services exposes allowlisted control over OS service units
// (typically vLLM model services on DGX). The /actions/service endpoint
// is the only entry point; this package is the security boundary.
//
// Security model (from V0_2_0_PLAN.md §A3):
//   - Unit name MUST appear verbatim in cfg.Allowed; no globs, no synthesis.
//   - Action MUST be one of {start, stop, restart, status}; enum-validated.
//   - No environment / no args ever flow from the client through to systemd.
//   - exec.Cmd never invokes a shell — args pass as a slice, so a malicious
//     unit name cannot inject ; rm -rf style payloads (in practice it can't
//     get past the allowlist check anyway, but defense in depth).
//   - The sudoers drop-in installed by `rt-node-agent install` restricts the
//     pattern to rt-vllm-*.service, so a config-allowlist misconfiguration
//     still can't escalate to controlling sshd/docker.
package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/redtorchinc/node-agent/internal/config"
)

// Action is the verb being requested. Closed enum to keep the wire surface
// minimal — no "reload" (would require unit file changes), no "kill", etc.
type Action string

const (
	ActionStart   Action = "start"
	ActionStop    Action = "stop"
	ActionRestart Action = "restart"
	ActionStatus  Action = "status"
)

// AllActions is the canonical ordered list for help text + capability discovery.
var AllActions = []Action{ActionStart, ActionStop, ActionRestart, ActionStatus}

// Errors returned by Manager.Do. Distinct types so the HTTP layer can map
// them to specific status codes without string matching.
var (
	ErrUnitNotAllowed  = errors.New("unit not in allowlist")
	ErrActionNotAllowed = errors.New("action not permitted for this unit")
	ErrUnknownAction    = errors.New("unknown action")
	ErrUnitNotFound     = errors.New("unit not known to systemd")
	ErrUnsupported      = errors.New("service control not supported on this OS")
)

// Result is what /actions/service returns to the caller.
type Result struct {
	Unit        string `json:"unit"`
	Action      string `json:"action"`
	ActiveState string `json:"active_state,omitempty"`
	SubState    string `json:"sub_state,omitempty"`
	MainPID     int    `json:"main_pid,omitempty"`
	MemoryMB    int64  `json:"memory_mb,omitempty"`
	TookMS      int64  `json:"took_ms"`
}

// State is the per-unit snapshot used both by Do(..., Status) and by the
// Manager.Snapshot helper that feeds /health.services[].
type State struct {
	Unit        string
	ActiveState string
	SubState    string
	MainPID     int
	MemoryMB    int64
}

// Manager is the platform-agnostic interface. systemd_linux.go implements
// it; stub_other.go provides a 501-returning stub everywhere else.
type Manager interface {
	// Do performs action on unit. Caller must have already validated the
	// allowlist (Do re-validates as defense in depth).
	Do(ctx context.Context, unit string, action Action) (Result, error)

	// Snapshot returns one State per allowlisted unit. Used by the health
	// reporter to populate /health.services[]. Errors are absorbed (state
	// for an unreachable unit is the zero value); we'd rather under-report
	// than fail /health on a transient systemd hiccup.
	Snapshot(ctx context.Context) []State

	// Capabilities returns the allowlist for /capabilities consumers.
	Capabilities() []config.ServiceAllowedEntry
}

// FromConfig wires a Manager from config.Services. Returns nil when no
// allowlist is configured — callers should treat that as "feature off"
// rather than erroring; same convention as platforms.
func FromConfig(cfg config.ServicesConfig) Manager {
	if len(cfg.Allowed) == 0 {
		return nil
	}
	return newManager(cfg)
}

// validate is shared by every Manager implementation.
//
// Returns the matched ServiceAllowedEntry on success so caller can re-read
// the per-unit actions list without rescanning.
func validate(cfg config.ServicesConfig, unit string, action Action) (config.ServiceAllowedEntry, error) {
	if !isValidAction(action) {
		return config.ServiceAllowedEntry{}, fmt.Errorf("%w: %q", ErrUnknownAction, action)
	}
	for _, e := range cfg.Allowed {
		if e.Name != unit {
			continue
		}
		if len(e.Actions) == 0 {
			// Empty list = all actions permitted on this unit.
			return e, nil
		}
		for _, a := range e.Actions {
			if Action(a) == action {
				return e, nil
			}
		}
		return e, fmt.Errorf("%w: %s on %s", ErrActionNotAllowed, action, unit)
	}
	return config.ServiceAllowedEntry{}, fmt.Errorf("%w: %s", ErrUnitNotAllowed, unit)
}

func isValidAction(a Action) bool {
	for _, v := range AllActions {
		if a == v {
			return true
		}
	}
	return false
}
