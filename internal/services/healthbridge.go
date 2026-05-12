package services

import (
	"context"

	"github.com/redtorchinc/node-agent/internal/health"
)

// HealthBridge adapts a Manager to health.ServicesReporter. Lives here
// rather than in health/ so the health package stays free of dependencies
// on services (which itself depends on config — keeps health's import
// graph small).
type HealthBridge struct {
	M Manager
}

// Snapshot implements health.ServicesReporter.
func (h HealthBridge) Snapshot(ctx context.Context) []health.ServiceState {
	if h.M == nil {
		return []health.ServiceState{}
	}
	src := h.M.Snapshot(ctx)
	out := make([]health.ServiceState, 0, len(src))
	for _, s := range src {
		out = append(out, health.ServiceState{
			Unit:        s.Unit,
			ActiveState: s.ActiveState,
			SubState:    s.SubState,
			MainPID:     s.MainPID,
			MemoryMB:    s.MemoryMB,
		})
	}
	return out
}
