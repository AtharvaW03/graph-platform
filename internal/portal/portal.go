// Package portal is the self-service key portal: an engineer signs in with
// their org identity (OIDC - Entra ID in this deployment), mints their
// personal API key for the hosted MCP endpoint, and can revoke it. One
// active key per person; keys expire at the start of each calendar month
// (see internal/keys), so renewal forces a fresh SSO login - a disabled
// account cannot renew.
//
// The portal carries its own session auth (signed cookie set after OIDC),
// so it mounts OUTSIDE the query API's bearer-token middleware. Nothing
// Microsoft-specific lives here: any standards-compliant OIDC issuer works,
// which is also how the tests drive the flow without a real tenant.
package portal

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"a1-knowledge-graph/internal/keys"

	oidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Identity is what the portal needs from a completed SSO login.
type Identity struct {
	Email string
	Name  string
}

// Authenticator abstracts the OIDC dance so tests can stub it. NewOIDC
// returns the real implementation.
type Authenticator interface {
	// AuthCodeURL returns the provider URL to redirect the browser to.
	AuthCodeURL(state string) string
	// Exchange trades the callback code for a verified identity.
	Exchange(ctx context.Context, code string) (Identity, error)
}

// KeyService is the slice of *keys.Store the portal uses; stubbed in tests.
type KeyService interface {
	Mint(ctx context.Context, owner, ownerName string) (string, keys.Key, error)
	ActiveKey(ctx context.Context, owner string) (keys.Key, bool, error)
	RevokeOwn(ctx context.Context, owner string) (bool, error)
}

// Config carries the OIDC client registration values, all from env.
type Config struct {
	Issuer       string // e.g. https://login.microsoftonline.com/<tenant>/v2.0
	ClientID     string
	ClientSecret string
	RedirectURL  string // https://<host>/portal/callback
}

// Enabled reports whether every required value is present.
func (c Config) Enabled() bool {
	return c.Issuer != "" && c.ClientID != "" && c.ClientSecret != "" && c.RedirectURL != ""
}

// NewOIDC builds the real Authenticator via issuer discovery.
func NewOIDC(ctx context.Context, cfg Config) (Authenticator, error) {
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery against %s: %w", cfg.Issuer, err)
	}
	return &oidcAuthenticator{
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
	}, nil
}

type oidcAuthenticator struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

func (a *oidcAuthenticator) AuthCodeURL(state string) string {
	return a.oauth.AuthCodeURL(state)
}

func (a *oidcAuthenticator) Exchange(ctx context.Context, code string) (Identity, error) {
	token, err := a.oauth.Exchange(ctx, code)
	if err != nil {
		return Identity{}, fmt.Errorf("code exchange: %w", err)
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		return Identity{}, fmt.Errorf("no id_token in token response")
	}
	idToken, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		return Identity{}, fmt.Errorf("id token verify: %w", err)
	}
	var claims struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("claims: %w", err)
	}
	// Entra frequently omits the email claim; preferred_username is the UPN
	// (an email-shaped org identifier) and serves as the stable fallback.
	email := claims.Email
	if email == "" {
		email = claims.PreferredUsername
	}
	if email == "" {
		return Identity{}, fmt.Errorf("id token carries neither email nor preferred_username")
	}
	return Identity{Email: email, Name: claims.Name}, nil
}

// Handler serves the portal routes.
type Handler struct {
	auth          Authenticator
	keys          KeyService
	sessionSecret []byte
	secureCookies bool
	now           func() time.Time
}

const (
	sessionCookie = "a1kg_portal_session"
	stateCookie   = "a1kg_portal_state"
	sessionTTL    = 8 * time.Hour
)

// NewHandler wires the portal. sessionSecret signs session cookies; derive
// it from a dedicated env var. secureCookies should be true whenever the
// portal is reached over HTTPS (i.e. any real deployment).
func NewHandler(auth Authenticator, ks KeyService, sessionSecret []byte, secureCookies bool) *Handler {
	return &Handler{
		auth:          auth,
		keys:          ks,
		sessionSecret: sessionSecret,
		secureCookies: secureCookies,
		now:           time.Now,
	}
}

// Routes returns the portal mux, rooted at /portal.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /portal", h.home)
	mux.HandleFunc("GET /portal/{$}", h.home)
	mux.HandleFunc("GET /portal/login", h.login)
	mux.HandleFunc("GET /portal/callback", h.callback)
	mux.HandleFunc("POST /portal/keys", h.mint)
	mux.HandleFunc("POST /portal/keys/revoke", h.revoke)
	mux.HandleFunc("GET /portal/logout", h.logout)
	return mux
}

// -------- session cookies --------

type session struct {
	Email   string    `json:"e"`
	Name    string    `json:"n"`
	Expires time.Time `json:"x"`
}

func (h *Handler) sign(payload []byte) string {
	mac := hmac.New(sha256.New, h.sessionSecret)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + hex.EncodeToString(mac.Sum(nil))
}

func (h *Handler) verify(value string) ([]byte, bool) {
	payloadB64, sig, found := strings.Cut(value, ".")
	if !found {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, h.sessionSecret)
	mac.Write(payload)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return nil, false
	}
	return payload, true
}

func (h *Handler) setSession(w http.ResponseWriter, id Identity) {
	payload, _ := json.Marshal(session{Email: id.Email, Name: id.Name, Expires: h.now().Add(sessionTTL)})
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookie,
		Value: h.sign(payload),
		// Root path, not /portal: the admin surface reads this same session
		// to identify administrators.
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (h *Handler) currentSession(r *http.Request) (session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return session{}, false
	}
	payload, ok := h.verify(c.Value)
	if !ok {
		return session{}, false
	}
	var s session
	if err := json.Unmarshal(payload, &s); err != nil {
		return session{}, false
	}
	if h.now().After(s.Expires) {
		return session{}, false
	}
	return s, true
}

// SessionEmail exposes the signed-in identity to other surfaces (the admin
// authorizer). Reports false when there is no valid session, so callers
// never have to know how sessions are encoded.
func (h *Handler) SessionEmail(r *http.Request) (string, bool) {
	s, ok := h.currentSession(r)
	if !ok {
		return "", false
	}
	return s.Email, true
}

// -------- OIDC flow --------

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		http.Error(w, "entropy unavailable", http.StatusInternalServerError)
		return
	}
	state := hex.EncodeToString(b[:])
	// returnTo lets a caller (the admin surface's sign-in link, in
	// particular) land back where the user started instead of always at
	// /portal. Restricted to a fixed allowlist rather than trusting the
	// query value verbatim - an unchecked redirect target here would be an
	// open-redirect through this login flow.
	payload, _ := json.Marshal(loginState{State: state, ReturnTo: sanitizeReturnTo(r.URL.Query().Get("return_to"))})
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    h.sign(payload),
		Path:     "/portal",
		HttpOnly: true,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteLaxMode, // Lax: the callback arrives as a top-level cross-site redirect
		MaxAge:   300,
	})
	http.Redirect(w, r, h.auth.AuthCodeURL(state), http.StatusFound)
}

// loginState is the state cookie's payload: the random value verified
// against the OIDC provider's echoed `state` parameter, plus where to send
// the user once sign-in completes.
type loginState struct {
	State    string `json:"s"`
	ReturnTo string `json:"r"`
}

// sanitizeReturnTo only allows known internal destinations. This endpoint
// completes third-party sign-in, so an unvalidated redirect target here
// would let an attacker-crafted /portal/login?return_to=... link bounce a
// freshly authenticated user to an attacker-controlled page.
func sanitizeReturnTo(v string) string {
	if v == "/admin" || strings.HasPrefix(v, "/admin/") {
		return v
	}
	return "/portal"
}

func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(stateCookie)
	if err != nil {
		http.Error(w, "missing state - restart login", http.StatusBadRequest)
		return
	}
	payload, ok := h.verify(c.Value)
	if !ok {
		http.Error(w, "state mismatch - restart login", http.StatusBadRequest)
		return
	}
	var ls loginState
	if err := json.Unmarshal(payload, &ls); err != nil || r.URL.Query().Get("state") != ls.State {
		http.Error(w, "state mismatch - restart login", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	id, err := h.auth.Exchange(r.Context(), code)
	if err != nil {
		log.Printf("portal: exchange failed: %v", err)
		http.Error(w, "sign-in failed", http.StatusUnauthorized)
		return
	}
	// Clear the state cookie, set the session, and land back where the
	// flow started (default /portal).
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/portal", MaxAge: -1})
	h.setSession(w, id)
	dest := ls.ReturnTo
	if dest == "" {
		dest = "/portal"
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	// Path must match the one setSession used, or the browser keeps the
	// cookie and "sign out" silently does nothing.
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/portal", http.StatusFound)
}

// -------- pages and actions --------

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	s, ok := h.currentSession(r)
	if !ok {
		renderPage(w, pageData{SignedOut: true})
		return
	}
	data := pageData{Email: s.Email, Name: s.Name}
	if k, found, err := h.keys.ActiveKey(r.Context(), s.Email); err != nil {
		log.Printf("portal: active key lookup: %v", err)
		data.Error = "could not load key state - try again"
	} else if found {
		data.Key = &k
	}
	renderPage(w, data)
}

func (h *Handler) mint(w http.ResponseWriter, r *http.Request) {
	s, ok := h.currentSession(r)
	if !ok {
		http.Redirect(w, r, "/portal", http.StatusFound)
		return
	}
	plaintext, k, err := h.keys.Mint(r.Context(), s.Email, s.Name)
	if err != nil {
		log.Printf("portal: mint for %s: %v", s.Email, err)
		renderPage(w, pageData{Email: s.Email, Name: s.Name, Error: "minting failed - try again"})
		return
	}
	log.Printf("portal: key minted for %s (id %s)", s.Email, k.ID)
	renderPage(w, pageData{Email: s.Email, Name: s.Name, Key: &k, Plaintext: plaintext})
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	s, ok := h.currentSession(r)
	if !ok {
		http.Redirect(w, r, "/portal", http.StatusFound)
		return
	}
	if _, err := h.keys.RevokeOwn(r.Context(), s.Email); err != nil {
		log.Printf("portal: revoke for %s: %v", s.Email, err)
	} else {
		log.Printf("portal: key revoked for %s", s.Email)
	}
	http.Redirect(w, r, "/portal", http.StatusFound)
}

// -------- rendering --------

type pageData struct {
	SignedOut bool
	Email     string
	Name      string
	Key       *keys.Key
	Plaintext string // set only on the mint response - shown exactly once
	Error     string
}

var pageTmpl = template.Must(template.New("portal").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>A1 Knowledge Graph - API keys</title>
<style>
  :root { color-scheme: light dark; }
  body { font-family: system-ui, sans-serif; max-width: 40rem; margin: 3rem auto; padding: 0 1rem; line-height: 1.5; }
  h1 { font-size: 1.3rem; }
  code, .key { font-family: ui-monospace, Consolas, monospace; }
  .key { display: block; padding: .75rem; border: 1px solid #8886; border-radius: 6px; word-break: break-all; margin: .75rem 0; user-select: all; }
  .muted { opacity: .7; font-size: .9rem; }
  .warn { color: #b45309; }
  .error { color: #b91c1c; }
  button { padding: .45rem .9rem; border-radius: 6px; border: 1px solid #8888; cursor: pointer; }
  form { display: inline-block; margin-right: .5rem; }
  table { border-collapse: collapse; margin: .75rem 0; }
  td { padding: .2rem .8rem .2rem 0; }
</style>
</head>
<body>
<h1>A1 Knowledge Graph - API keys</h1>
{{if .SignedOut}}
  <p>Sign in with your org account to manage your personal API key for the
  knowledge-graph MCP endpoint.</p>
  <p><a href="/portal/login"><button>Sign in</button></a></p>
{{else}}
  <p class="muted">{{if .Name}}{{.Name}} - {{end}}{{.Email}} - <a href="/portal/logout">sign out</a></p>
  {{if .Error}}<p class="error">{{.Error}}</p>{{end}}
  {{if .Plaintext}}
    <p><strong>Your new key.</strong> Copy it now - it is shown only this once.</p>
    <span class="key">{{.Plaintext}}</span>
    <p class="muted">Add it to your MCP client:</p>
    <span class="key">claude mcp add --transport http a1-knowledge-graph https://&lt;host&gt;/mcp --header "Authorization: Bearer {{.Plaintext}}"</span>
  {{end}}
  {{if .Key}}
    <table>
      <tr><td class="muted">Key ID</td><td><code>{{.Key.ID}}</code></td></tr>
      <tr><td class="muted">Created</td><td>{{.Key.CreatedAt.Format "2006-01-02 15:04 UTC"}}</td></tr>
      <tr><td class="muted">Expires</td><td>{{.Key.ExpiresAt.Format "2006-01-02"}} (start of next month - re-mint then)</td></tr>
      {{if .Key.LastUsedAt}}<tr><td class="muted">Last used</td><td>{{.Key.LastUsedAt.Format "2006-01-02 15:04 UTC"}}</td></tr>{{end}}
    </table>
    <form method="post" action="/portal/keys"><button>Replace key</button></form>
    <form method="post" action="/portal/keys/revoke"><button>Revoke key</button></form>
    <p class="muted warn">Replacing or revoking takes effect within a minute.</p>
  {{else if not .Plaintext}}
    <p>You have no active key.</p>
    <form method="post" action="/portal/keys"><button>Generate key</button></form>
  {{end}}
{{end}}
</body>
</html>`))

func renderPage(w http.ResponseWriter, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := pageTmpl.Execute(w, data); err != nil {
		log.Printf("portal: render: %v", err)
	}
}
