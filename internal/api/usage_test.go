package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"a1-knowledge-graph/internal/httpmw"
	"a1-knowledge-graph/internal/usage"
)

type capturedSample struct {
	actor    string
	endpoint string
	repos    []string
}

type fakeRecorder struct{ samples []capturedSample }

func (f *fakeRecorder) Record(actor, endpoint string, repos []string) {
	f.samples = append(f.samples, capturedSample{actor, endpoint, repos})
}

// handlerWithStatus serves a fixed status so the "don't count failures"
// rule can be exercised.
func handlerWithStatus(code int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	})
}

func TestUsageRecording_AttributesAndScopes(t *testing.T) {
	rec := &fakeRecorder{}
	h := WithUsageRecording(handlerWithStatus(http.StatusOK), rec)

	req := httptest.NewRequest(http.MethodGet, "/search?q=x&repos=alpha,beta", nil)
	req = req.WithContext(httpmw.WithActor(req.Context(), "dev@org.example"))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if len(rec.samples) != 1 {
		t.Fatalf("recorded %d samples, want 1", len(rec.samples))
	}
	s := rec.samples[0]
	if s.actor != "dev@org.example" {
		t.Errorf("actor = %q, want the forwarded identity", s.actor)
	}
	if s.endpoint != "search" {
		t.Errorf("endpoint = %q, want \"search\"", s.endpoint)
	}
	if len(s.repos) != 2 || s.repos[0] != "alpha" || s.repos[1] != "beta" {
		t.Errorf("repos = %v, want [alpha beta]", s.repos)
	}
}

func TestUsageRecording_UnattributedFallsBackToInternal(t *testing.T) {
	rec := &fakeRecorder{}
	h := WithUsageRecording(handlerWithStatus(http.StatusOK), rec)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/hotspots", nil))

	if len(rec.samples) != 1 || rec.samples[0].actor != usage.ActorInternal {
		t.Errorf("unattributed request actor = %v, want %q", rec.samples, usage.ActorInternal)
	}
}

func TestUsageRecording_SkipsInfrastructurePaths(t *testing.T) {
	rec := &fakeRecorder{}
	h := WithUsageRecording(handlerWithStatus(http.StatusOK), rec)

	for _, path := range []string{"/health", "/ready", "/portal", "/portal/keys", "/admin/usage", "/keys/validate", "/feedback/stats"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	if len(rec.samples) != 0 {
		t.Errorf("recorded %d samples for infrastructure paths, want 0: %+v", len(rec.samples), rec.samples)
	}
}

func TestUsageRecording_SkipsFailedRequests(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusInternalServerError} {
		rec := &fakeRecorder{}
		h := WithUsageRecording(handlerWithStatus(code), rec)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/search?q=x", nil))
		if len(rec.samples) != 0 {
			t.Errorf("status %d recorded as usage", code)
		}
	}
}

func TestRecordableEndpoint_DropsSubjectSegment(t *testing.T) {
	// The subject (symbol name, repo) must never become part of the metric
	// name: it would explode cardinality and store what people searched for.
	got, ok := recordableEndpoint("/symbol/GetDepositService()")
	if !ok || got != "symbol" {
		t.Errorf("recordableEndpoint = %q,%v; want \"symbol\",true", got, ok)
	}
}
