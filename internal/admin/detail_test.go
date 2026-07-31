package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubGraph is a GraphStats that reports a fixed repository set.
type stubGraph struct {
	repos []RepoSize
	fresh []RepoFreshness
}

func (s stubGraph) ListRepositories(context.Context) ([]RepoSize, error) { return s.repos, nil }
func (s stubGraph) Freshness(context.Context) ([]RepoFreshness, error)   { return s.fresh, nil }

func detailHandler() *Handler {
	return NewHandler(Deps{
		Graph: stubGraph{
			repos: []RepoSize{{Name: "payments", Nodes: 4200}},
			fresh: []RepoFreshness{{Repo: "payments", AgeSeconds: 120, Stale: false}},
		},
	}, NewTokenSessionAuth(nil, "", "secret"), 0, 0)
}

func getAsAdmin(t *testing.T, h *Handler, path string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	var body map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec.Code, body
}

func TestRepoDetail_KnownRepository(t *testing.T) {
	code, body := getAsAdmin(t, detailHandler(), "/admin/api/repos/payments")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body["name"] != "payments" {
		t.Errorf("name = %v, want payments", body["name"])
	}
	if body["indexed"] != true {
		t.Errorf("indexed = %v, want true", body["indexed"])
	}
	if body["nodes"] != float64(4200) {
		t.Errorf("nodes = %v, want 4200", body["nodes"])
	}
}

// A repository nobody has indexed must answer "not in the graph" rather
// than 404 - that distinction is the whole point of the lookup when
// someone reports their repo is missing.
func TestRepoDetail_UnknownRepositoryReportsNotIndexed(t *testing.T) {
	code, body := getAsAdmin(t, detailHandler(), "/admin/api/repos/never-indexed")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body["indexed"] != false {
		t.Errorf("indexed = %v, want false for an unknown repository", body["indexed"])
	}
}

// Names with characters that need URL and shell care must round-trip.
func TestRepoDetail_EncodedName(t *testing.T) {
	h := NewHandler(Deps{
		Graph: stubGraph{repos: []RepoSize{{Name: "team/service", Nodes: 7}}},
	}, NewTokenSessionAuth(nil, "", "secret"), 0, 0)

	code, body := getAsAdmin(t, h, "/admin/api/repos/team%2Fservice")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body["name"] != "team/service" || body["indexed"] != true {
		t.Errorf("encoded name did not round-trip: %+v", body)
	}
}

func TestActorDetail_NoKeysIsNotAnError(t *testing.T) {
	code, body := getAsAdmin(t, detailHandler(), "/admin/api/actors/internal")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body["actor"] != "internal" {
		t.Errorf("actor = %v", body["actor"])
	}
	// Must be an empty array, never null - the UI iterates it directly.
	if _, ok := body["keys"].([]any); !ok {
		t.Errorf("keys = %#v, want an empty array", body["keys"])
	}
	if _, ok := body["repos"].([]any); !ok {
		t.Errorf("repos = %#v, want an empty array", body["repos"])
	}
}

func TestDetailEndpoints_RequireAuth(t *testing.T) {
	h := detailHandler()
	for _, path := range []string{"/admin/api/repos/payments", "/admin/api/actors/someone"} {
		rec := httptest.NewRecorder()
		h.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without credentials: %d, want 401", path, rec.Code)
		}
	}
}
