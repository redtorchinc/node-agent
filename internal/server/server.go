// Package server exposes the rt-node-agent HTTP surface.
//
// See SPEC.md §HTTP API for the full contract. Read endpoints are open on
// the LAN; mutating /actions/* endpoints require a Bearer token.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/redtorchinc/node-agent/internal/config"
	"github.com/redtorchinc/node-agent/internal/health"
	"github.com/redtorchinc/node-agent/internal/mode"
	"github.com/redtorchinc/node-agent/internal/ollama"
	"github.com/redtorchinc/node-agent/internal/services"
)

// Server wraps an http.Server + the shared dependencies its handlers read from.
type Server struct {
	cfg      config.Config
	reporter *health.Reporter
	ollama   *ollama.Client
	svcMgr   services.Manager
	modeMgr  *mode.Manager
	http     *http.Server
}

// New returns a configured Server. Does not listen until Run.
func New(cfg config.Config, reporter *health.Reporter) *Server {
	s := &Server{
		cfg:      cfg,
		reporter: reporter,
		ollama:   reporter.Ollama,
		svcMgr:   services.FromConfig(cfg.Services),
		modeMgr:  mode.New(cfg.TrainingMode.StateFile, int64(cfg.TrainingMode.GracePeriodS)),
	}
	// Restore any persisted training-mode state from disk. Safe to call
	// before serving — if state is stale (expected_duration + grace
	// exceeded), Restore clears it and logs a warning rather than carrying
	// it into a fresh run.
	s.modeMgr.Restore()
	reporter.SetModeReporter(s.modeMgr)

	// Wire the services snapshot into the health reporter so /health.services
	// is populated without health having to import internal/services.
	if s.svcMgr != nil {
		reporter.SetServicesReporter(services.HealthBridge{M: s.svcMgr})
	}
	mux := http.NewServeMux()
	s.routes(mux)
	s.http = &http.Server{
		Addr:              s.Addr(),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	return s
}

// Addr returns the bind address the server will (or did) listen on.
func (s *Server) Addr() string {
	return net.JoinHostPort(s.cfg.Bind, strconv.Itoa(s.cfg.Port))
}

// Run starts listening and blocks until ctx is cancelled, then gracefully
// shuts down the HTTP server with a 5s timeout.
func (s *Server) Run(ctx context.Context) error {
	// Start background scrapers before accepting traffic.
	s.reporter.StartBackground(ctx)

	errCh := make(chan error, 1)
	go func() {
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		slog.Info("rt-node-agent shutting down")
		return s.http.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}

// Handler returns the configured http.Handler. Exposed for tests.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	return mux
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/version", s.handleVersion)
	mux.HandleFunc("/capabilities", s.handleCapabilities)
	mux.HandleFunc("/actions/unload-model", s.requireToken(s.handleUnload))
	mux.HandleFunc("/actions/service", s.requireToken(s.handleServiceAction))
	mux.HandleFunc("/actions/training-mode", s.requireToken(s.handleTrainingMode))
	if s.cfg.MetricsEnabled {
		mux.HandleFunc("/metrics", s.handleMetrics)
	}
	mux.HandleFunc("/", s.handleRoot)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "rt-node-agent — see SPEC.md and docs/")
	fmt.Fprintln(w, "read:    GET /health, GET /version, GET /capabilities, GET /metrics")
	fmt.Fprintln(w, "actions: POST /actions/unload-model, POST /actions/service")
}
