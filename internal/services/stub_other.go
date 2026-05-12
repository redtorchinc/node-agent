//go:build !linux

package services

import (
	"context"

	"github.com/redtorchinc/node-agent/internal/config"
)

// stubManager returns ErrUnsupported for every Do call. /health.services
// remains empty. Used on macOS/Windows v0.2.0; launchd/SCM control will
// land in v0.3.0 if there's demand.
type stubManager struct {
	cfg config.ServicesConfig
}

func newManager(cfg config.ServicesConfig) Manager { return &stubManager{cfg: cfg} }

func (m *stubManager) Capabilities() []config.ServiceAllowedEntry { return m.cfg.Allowed }

func (m *stubManager) Do(_ context.Context, _ string, _ Action) (Result, error) {
	return Result{}, ErrUnsupported
}

func (m *stubManager) Snapshot(_ context.Context) []State { return nil }
