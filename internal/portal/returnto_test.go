package portal

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// This reproduces the exact browser loop reported live: an unauthenticated
// visitor hits /admin, follows that page's "Sign in" link
// (/portal/login?return_to=/admin), completes the OIDC round trip, and must
// land back on /admin - not always at /portal regardless of where they
// started.
func TestLogin_ReturnToCarriesThroughCallback(t *testing.T) {
	h, _ := newTestHandler()
	mux := h.Routes()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/portal/login?return_to=/admin", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("login: %d, want 302", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	state := loc.Query().Get("state")
	var stateCk *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == stateCookie {
			stateCk = c
		}
	}
	if stateCk == nil {
		t.Fatal("login did not set a state cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/portal/callback?state="+url.QueryEscape(state)+"&code=good-code", nil)
	req.AddCookie(stateCk)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("callback: %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/admin" {
		t.Errorf("post-login redirect = %q, want /admin (this is the exact bug reported: landing on /portal instead)", got)
	}
}

// No return_to at all - the plain portal login must keep working exactly
// as before (default destination /portal).
func TestLogin_NoReturnToDefaultsToPortal(t *testing.T) {
	h, _ := newTestHandler()
	sess := signIn(t, h) // uses plain /portal/login, no return_to
	if sess == nil {
		t.Fatal("sign-in did not produce a session")
	}
}

// An attacker-supplied return_to must never be honored verbatim - this
// endpoint completes third-party sign-in, so an unvalidated redirect here
// is an open-redirect through a trusted-looking URL.
func TestLogin_ReturnToRejectsUnknownDestinations(t *testing.T) {
	h, _ := newTestHandler()
	mux := h.Routes()

	for _, evil := range []string{
		"https://evil.example/steal",
		"//evil.example/steal",
		"/../etc/passwd",
		"/some-other-internal-page",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/portal/login?return_to="+url.QueryEscape(evil), nil))
		loc, _ := url.Parse(rec.Header().Get("Location"))
		state := loc.Query().Get("state")
		var stateCk *http.Cookie
		for _, c := range rec.Result().Cookies() {
			if c.Name == stateCookie {
				stateCk = c
			}
		}
		req := httptest.NewRequest(http.MethodGet, "/portal/callback?state="+url.QueryEscape(state)+"&code=good-code", nil)
		req.AddCookie(stateCk)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		dest := rec.Header().Get("Location")
		if strings.Contains(dest, "evil.example") || dest == evil {
			t.Errorf("return_to=%q was honored (redirected to %q) - open redirect", evil, dest)
		}
		if dest != "/portal" {
			t.Errorf("unrecognized return_to=%q redirected to %q, want the safe default /portal", evil, dest)
		}
	}
}
