package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// requireToken wraps h so it only runs when the request carries the
// configured Bearer token. Returns:
//
//	503 "token not configured" if no token is set — signals "not yet provisioned"
//	                            rather than "auth rejected".
//	401 "unauthorized"         if the header is missing or malformed.
//	401 "unauthorized"         if the token does not match (constant-time compare).
func (s *Server) requireToken(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		expected := s.cfg.Token
		if expected == "" {
			http.Error(w, "token not configured", http.StatusServiceUnavailable)
			return
		}
		got := bearer(r.Header.Get("Authorization"))
		if got == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func bearer(h string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}
