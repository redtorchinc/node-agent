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
	"github.com/redtorchinc/node-agent/internal/ollama"
)

// Server wraps an http.Server + the shared dependencies its handlers read from.
type Server struct {
	cfg      config.Config
	reporter *health.Reporter
	ollama   *ollama.Client
	http     *http.Server
}

// New returns a configured Server. Does not listen until Run.
func New(cfg config.Config, reporter *health.Reporter) *Server {
	s := &Server{
		cfg:      cfg,
		reporter: reporter,
		ollama:   reporter.Ollama,
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
	mux.HandleFunc("/actions/unload-model", s.requireToken(s.handleUnload))
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
	fmt.Fprintln(w, "rt-node-agent — see SPEC.md")
	fmt.Fprintln(w, "endpoints: GET /health, GET /version, POST /actions/unload-model")
}
