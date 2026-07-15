// Package httpmw holds HTTP middleware shared across this module's servers
// (query-service's REST API, mcp-server's HTTP mode) without dragging either
// server's domain packages into the other.
package httpmw

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
)

// WithAuth wraps h with static bearer-token authentication. An empty token
// disables auth (open mode, for local development). /health and /ready stay
// unauthenticated so load balancers and uptime probes work without
// credentials.
func WithAuth(h http.Handler, token string) http.Handler {
	if token == "" {
		return h
	}
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/ready" {
			h.ServeHTTP(w, r)
			return
		}
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, expected) != 1 {
			writeErr(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		h.ServeHTTP(w, r)
	})
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": msg}); err != nil {
		log.Printf("write json: %v", err)
	}
}
