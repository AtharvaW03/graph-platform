package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubSessions struct {
	email string
	ok    bool
}

func (s stubSessions) SessionEmail(*http.Request) (string, bool) { return s.email, s.ok }

func TestAuthorize_Token(t *testing.T) {
	a := NewTokenSessionAuth(nil, "", "admin-secret")

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Authorization", "Bearer admin-secret")
	actor, ok := a.Authorize(req)
	if !ok || actor != "token-admin" {
		t.Errorf("valid token: actor=%q ok=%v; want token-admin,true", actor, ok)
	}

	req.Header.Set("Authorization", "Bearer wrong")
	if _, ok := a.Authorize(req); ok {
		t.Error("wrong token authorized")
	}

	if _, ok := a.Authorize(httptest.NewRequest(http.MethodGet, "/admin", nil)); ok {
		t.Error("no credential authorized")
	}
}

func TestAuthorize_SessionAllowlist(t *testing.T) {
	a := NewTokenSessionAuth(stubSessions{email: "Boss@Org.Example", ok: true}, "boss@org.example, other@org.example", "")

	actor, ok := a.Authorize(httptest.NewRequest(http.MethodGet, "/admin", nil))
	if !ok {
		t.Fatal("allowlisted admin (case-differing) not authorized")
	}
	if actor != "Boss@Org.Example" {
		t.Errorf("actor = %q, want the session email for the audit trail", actor)
	}
}

func TestAuthorize_SessionNotOnAllowlist(t *testing.T) {
	a := NewTokenSessionAuth(stubSessions{email: "dev@org.example", ok: true}, "boss@org.example", "")
	if _, ok := a.Authorize(httptest.NewRequest(http.MethodGet, "/admin", nil)); ok {
		t.Error("non-admin signed-in user was authorized - the allowlist is not being enforced")
	}
}

func TestAuthorize_NoSessionNoToken(t *testing.T) {
	a := NewTokenSessionAuth(stubSessions{ok: false}, "boss@org.example", "")
	if _, ok := a.Authorize(httptest.NewRequest(http.MethodGet, "/admin", nil)); ok {
		t.Error("signed-out request authorized")
	}
}

func TestEnabled(t *testing.T) {
	cases := []struct {
		name string
		auth *TokenSessionAuth
		want bool
	}{
		{"nothing configured", NewTokenSessionAuth(nil, "", ""), false},
		{"allowlist without sessions", NewTokenSessionAuth(nil, "boss@org.example", ""), false},
		{"sessions without allowlist", NewTokenSessionAuth(stubSessions{}, "", ""), false},
		{"token only", NewTokenSessionAuth(nil, "", "t"), true},
		{"sessions plus allowlist", NewTokenSessionAuth(stubSessions{}, "boss@org.example", ""), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.auth.Enabled(); got != tc.want {
				t.Errorf("Enabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// The admin surface must reject unauthorized callers on every route,
// including the read-only ones.
func TestRoutes_AllRequireAuth(t *testing.T) {
	h := NewHandler(Deps{}, NewTokenSessionAuth(nil, "", "secret"), 0, 0)
	mux := h.Routes()

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/admin"},
		{http.MethodGet, "/admin/api/overview"},
		{http.MethodGet, "/admin/api/usage"},
		{http.MethodGet, "/admin/api/anomalies"},
		{http.MethodGet, "/admin/api/keys"},
		{http.MethodGet, "/admin/api/audit"},
		{http.MethodPost, "/admin/api/keys/revoke"},
		{http.MethodPost, "/admin/api/indexing/pause"},
		{http.MethodPost, "/admin/api/indexing/resume"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without credentials: %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

func TestDashboard_ServesHTMLToAuthorizedAdmin(t *testing.T) {
	h := NewHandler(Deps{}, NewTokenSessionAuth(nil, "", "secret"), 0, 0)
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard: %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content type = %q", ct)
	}
}

// A browser hitting the dashboard signed-out should get a sign-in page,
// not a JSON error blob.
func TestDashboard_SignedOutBrowserGetsSignInPage(t *testing.T) {
	h := NewHandler(Deps{}, NewTokenSessionAuth(nil, "", "secret"), 0, 0)
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if body := rec.Body.String(); !contains(body, "Sign in") {
		t.Errorf("signed-out browser did not get a sign-in page: %q", body)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
