package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A regression test for the middleware-ordering bug: r.PathValue is only
// populated on the exact *http.Request the pattern-matching mux received.
// Any layer that calls r.WithContext (a new *http.Request, same values)
// BETWEEN WithUsageRecording and the mux would see PathValue forever
// unset - the existing query-param test never caught this because query
// scoping reads r.URL, which is unaffected by request copies.
//
// This drives a real http.ServeMux with a "{repo}" pattern, exactly like
// /overview/{repo} and /dependencies/{repo}, so PathValue is populated for
// real rather than asserted against a hand-built request.
func TestUsageRecording_PathScopedRoute(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /overview/{repo}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := &fakeRecorder{}
	recorded := WithUsageRecording(mux, rec)

	req := httptest.NewRequest(http.MethodGet, "/overview/payments-service", nil)
	recorded.ServeHTTP(httptest.NewRecorder(), req)

	if len(rec.samples) != 1 {
		t.Fatalf("recorded %d samples, want 1", len(rec.samples))
	}
	if got := rec.samples[0].repos; len(got) != 1 || got[0] != "payments-service" {
		t.Errorf("repos = %v, want [payments-service] - a path-scoped route's repo was not captured", got)
	}
}

// Reproduces exactly how cmd/query-service composes the stack: something
// that copies the request (r.WithContext, as WithRequestTimeout does)
// placed OUTSIDE WithUsageRecording must not break PathValue capture. This
// is the shape that was actually broken.
func TestUsageRecording_SurvivesAnOuterContextCopy(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dependencies/{repo}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := &fakeRecorder{}
	stack := contextCopyingMiddleware(WithUsageRecording(mux, rec))

	req := httptest.NewRequest(http.MethodGet, "/dependencies/auth-service", nil)
	stack.ServeHTTP(httptest.NewRecorder(), req)

	if len(rec.samples) != 1 || len(rec.samples[0].repos) != 1 || rec.samples[0].repos[0] != "auth-service" {
		t.Errorf("samples = %+v, want one sample scoped to auth-service - PathValue was lost across the outer request copy", rec.samples)
	}
}

// contextCopyingMiddleware mimics WithRequestTimeout's r.WithContext call
// without pulling in a real timeout - it's the minimal reproduction of the
// shape that broke path-scoped attribution.
func contextCopyingMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(r.Context()))
	})
}
