package portal

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"a1-knowledge-graph/internal/keys"
)

// fakeAuth completes the OIDC dance without a provider.
type fakeAuth struct {
	identity Identity
	fail     bool
}

func (f *fakeAuth) AuthCodeURL(state string) string {
	return "https://idp.example.com/authorize?state=" + url.QueryEscape(state)
}

func (f *fakeAuth) Exchange(_ context.Context, code string) (Identity, error) {
	if f.fail || code != "good-code" {
		return Identity{}, fmt.Errorf("bad code")
	}
	return f.identity, nil
}

// fakeKeys is an in-memory KeyService.
type fakeKeys struct {
	active map[string]keys.Key
	minted int
}

func newFakeKeys() *fakeKeys { return &fakeKeys{active: map[string]keys.Key{}} }

func (f *fakeKeys) Mint(_ context.Context, owner, ownerName string) (string, keys.Key, error) {
	f.minted++
	k := keys.Key{
		ID:        fmt.Sprintf("id%d", f.minted),
		Owner:     strings.ToLower(owner),
		OwnerName: ownerName,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(720 * time.Hour),
	}
	f.active[k.Owner] = k
	return keys.Prefix + "testkey", k, nil
}

func (f *fakeKeys) ActiveKey(_ context.Context, owner string) (keys.Key, bool, error) {
	k, ok := f.active[strings.ToLower(owner)]
	return k, ok, nil
}

func (f *fakeKeys) RevokeOwn(_ context.Context, owner string) (bool, error) {
	owner = strings.ToLower(owner)
	_, ok := f.active[owner]
	delete(f.active, owner)
	return ok, nil
}

func newTestHandler() (*Handler, *fakeKeys) {
	fk := newFakeKeys()
	h := NewHandler(
		&fakeAuth{identity: Identity{Email: "Dev@org.example", Name: "Dev One"}},
		fk,
		[]byte("test-session-secret"),
		false,
	)
	return h, fk
}

// signIn drives login -> callback and returns the session cookie.
func signIn(t *testing.T, h *Handler) *http.Cookie {
	t.Helper()
	mux := h.Routes()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/portal/login", nil))
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
	if stateCk == nil || state == "" {
		t.Fatal("login did not set state cookie / state param")
	}

	req := httptest.NewRequest(http.MethodGet, "/portal/callback?state="+url.QueryEscape(state)+"&code=good-code", nil)
	req.AddCookie(stateCk)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: %d, want 302 (body %q)", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	t.Fatal("callback did not set a session cookie")
	return nil
}

func TestSignedOutHomeShowsLogin(t *testing.T) {
	h, _ := newTestHandler()
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/portal", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Sign in") {
		t.Errorf("signed-out home: %d, body should offer sign-in", rec.Code)
	}
}

func TestFullFlow_LoginMintRevoke(t *testing.T) {
	h, fk := newTestHandler()
	mux := h.Routes()
	sess := signIn(t, h)

	// Home now shows the signed-in state with no key.
	req := httptest.NewRequest(http.MethodGet, "/portal", nil)
	req.AddCookie(sess)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "dev@org.example") && !strings.Contains(rec.Body.String(), "Dev@org.example") {
		t.Errorf("home does not show identity: %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Generate key") {
		t.Error("home should offer key generation when none exists")
	}

	// Mint: plaintext appears exactly once, in this response.
	req = httptest.NewRequest(http.MethodPost, "/portal/keys", nil)
	req.AddCookie(sess)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), keys.Prefix+"testkey") {
		t.Error("mint response does not show the plaintext key")
	}
	if fk.minted != 1 {
		t.Errorf("minted %d keys, want 1", fk.minted)
	}

	// Home after mint: metadata, no plaintext.
	req = httptest.NewRequest(http.MethodGet, "/portal", nil)
	req.AddCookie(sess)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), keys.Prefix+"testkey") {
		t.Error("plaintext key visible on a later page load - must be shown only once")
	}
	if !strings.Contains(rec.Body.String(), "Revoke key") {
		t.Error("home should offer revoke when a key exists")
	}

	// Revoke.
	req = httptest.NewRequest(http.MethodPost, "/portal/keys/revoke", nil)
	req.AddCookie(sess)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Errorf("revoke: %d, want 302", rec.Code)
	}
	if len(fk.active) != 0 {
		t.Error("key still active after revoke")
	}
}

func TestCallbackRejectsStateMismatch(t *testing.T) {
	h, _ := newTestHandler()
	mux := h.Routes()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/portal/login", nil))
	var stateCk *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == stateCookie {
			stateCk = c
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/portal/callback?state=attacker-forged&code=good-code", nil)
	req.AddCookie(stateCk)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("forged state: %d, want 400", rec.Code)
	}
}

func TestMintWithoutSessionRedirects(t *testing.T) {
	h, fk := newTestHandler()
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/portal/keys", nil))
	if rec.Code != http.StatusFound {
		t.Errorf("mint without session: %d, want 302 redirect", rec.Code)
	}
	if fk.minted != 0 {
		t.Error("mint happened without a session")
	}
}

func TestTamperedSessionRejected(t *testing.T) {
	h, _ := newTestHandler()
	sess := signIn(t, h)

	// Flip a character in the cookie value; the HMAC must fail and the page
	// must fall back to signed-out.
	tampered := *sess
	tampered.Value = strings.Replace(tampered.Value, tampered.Value[5:6], "x", 1)
	if tampered.Value == sess.Value {
		tampered.Value = tampered.Value[:5] + "y" + tampered.Value[6:]
	}
	req := httptest.NewRequest(http.MethodGet, "/portal", nil)
	req.AddCookie(&tampered)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Sign in") {
		t.Error("tampered session cookie was accepted")
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	h, _ := newTestHandler()
	sess := signIn(t, h)

	h.now = func() time.Time { return time.Now().Add(sessionTTL + time.Hour) }
	req := httptest.NewRequest(http.MethodGet, "/portal", nil)
	req.AddCookie(sess)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Sign in") {
		t.Error("expired session still signed in")
	}
}
