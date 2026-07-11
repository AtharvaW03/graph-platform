package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseRepos(t *testing.T) {
	cases := []struct {
		query string
		want  []string
	}{
		{"", nil},
		{"repos=orders-service", []string{"orders-service"}},
		{"repos=a,b,c", []string{"a", "b", "c"}},
		{"repos=a,%20b%20,", []string{"a", "b"}},                // trims and drops empties
		{"repo=legacy", []string{"legacy"}},                     // legacy single-repo param
		{"repos=a,b&repo=legacy", []string{"a", "b", "legacy"}}, // both merge
	}
	for _, c := range cases {
		q, err := url.ParseQuery(c.query)
		if err != nil {
			t.Fatalf("bad test query %q: %v", c.query, err)
		}
		if got := parseRepos(q); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseRepos(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestWithAuth(t *testing.T) {
	const token = "secret-token"

	cases := []struct {
		name       string
		token      string // configured on the server
		path       string
		authHeader string
		wantStatus int
	}{
		{"open mode passes without header", "", "/search", "", http.StatusOK},
		{"missing header rejected", token, "/search", "", http.StatusUnauthorized},
		{"wrong token rejected", token, "/search", "Bearer wrong", http.StatusUnauthorized},
		{"token without Bearer prefix rejected", token, "/search", token, http.StatusUnauthorized},
		{"correct token passes", token, "/search", "Bearer " + token, http.StatusOK},
		{"health bypasses auth", token, "/health", "", http.StatusOK},
		{"ready bypasses auth", token, "/ready", "", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := WithAuth(okHandler(), tc.token)
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("got status %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

func TestWithCORS(t *testing.T) {
	const origin = "https://ui.example.com"

	t.Run("disabled when origin empty", func(t *testing.T) {
		h := WithCORS(okHandler(), "")
		req := httptest.NewRequest(http.MethodGet, "/search", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("expected no CORS headers, got Allow-Origin %q", got)
		}
	})

	t.Run("preflight answered for trusted origin", func(t *testing.T) {
		h := WithCORS(okHandler(), origin)
		req := httptest.NewRequest(http.MethodOptions, "/search", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got status %d, want %d", rec.Code, http.StatusNoContent)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
			t.Fatalf("Allow-Origin = %q, want %q", got, origin)
		}
		if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Authorization" {
			t.Fatalf("Allow-Headers = %q, want Authorization", got)
		}
	})

	t.Run("untrusted origin gets no CORS headers", func(t *testing.T) {
		h := WithCORS(okHandler(), origin)
		req := httptest.NewRequest(http.MethodGet, "/search", nil)
		req.Header.Set("Origin", "https://evil.example.com")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("expected no Allow-Origin for untrusted origin, got %q", got)
		}
	})
}

func TestWithRequestTimeout(t *testing.T) {
	h := WithRequestTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Error("expected request context to carry a deadline")
		}
		if remaining := time.Until(deadline); remaining > time.Minute {
			t.Errorf("deadline too far out: %s", remaining)
		}
		w.WriteHeader(http.StatusOK)
	}), 30*time.Second)

	req := httptest.NewRequest(http.MethodGet, "/search", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
}

// fakeReadiness lets tests control /ready's outcome without a live Neo4j.
type fakeReadiness struct{ err error }

func (f fakeReadiness) VerifyConnectivity(context.Context) error { return f.err }

func TestReadyCheck(t *testing.T) {
	t.Run("ready when the dependency check passes", func(t *testing.T) {
		s := NewServer(nil, fakeReadiness{})
		req := httptest.NewRequest(http.MethodGet, "/ready", nil)
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200", rec.Code)
		}
	})

	t.Run("not ready when the dependency check fails", func(t *testing.T) {
		s := NewServer(nil, fakeReadiness{err: errors.New("neo4j unreachable at 10.0.4.12:7687")})
		req := httptest.NewRequest(http.MethodGet, "/ready", nil)
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("got status %d, want 503", rec.Code)
		}
		// /ready is unauthenticated (same as /health) - the body must never
		// carry driver error detail (hostnames, ports) out to the network.
		body := rec.Body.String()
		if strings.Contains(body, "10.0.4.12") || strings.Contains(body, "reason") {
			t.Errorf("response body leaks internal detail: %s", body)
		}
	})

	t.Run("nil Readiness always reports ready", func(t *testing.T) {
		s := NewServer(nil, nil)
		req := httptest.NewRequest(http.MethodGet, "/ready", nil)
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200", rec.Code)
		}
	})
}
