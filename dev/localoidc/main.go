// Command localoidc is a throwaway OpenID Connect provider for exercising
// the key portal and admin dashboard (internal/portal, internal/admin)
// without a real identity provider - no org SSO config, no network access,
// stdlib only.
//
// NOT FOR ANY DEPLOYED USE. It authenticates nobody: whatever email you
// type into its sign-in form is who you become. It exists purely to let
// OIDC_* point at something during local development. See the "Local
// admin/portal testing" section of the root README for the full recipe.
//
// Usage:
//
//	go run ./dev/localoidc
//	go run ./dev/localoidc &   # background, then start query-service
//
// The listen address (and therefore the issuer URL query-service must be
// given) defaults to 127.0.0.1:9000 and is overridable with LOCALOIDC_ADDR
// if that port is already taken - both the OIDC_ISSUER value you export
// and this program derive from the same setting, so they can never drift
// out of sync the way two hardcoded constants could.
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const kid = "localoidc-key-1"

var (
	addr     = envOr("LOCALOIDC_ADDR", "127.0.0.1:9000")
	issuer   = "http://" + addr
	clientID = envOr("LOCALOIDC_CLIENT_ID", "a1kg-local")

	key *rsa.PrivateKey
	mu  sync.Mutex
	// pending maps an issued auth code to the identity that requested it,
	// consumed exactly once by /token.
	pending = map[string]identity{}
)

type identity struct {
	Email string
	Name  string
}

func main() {
	var err error
	key, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/.well-known/openid-configuration", discovery)
	http.HandleFunc("/authorize", authorize)
	http.HandleFunc("/token", token)
	http.HandleFunc("/jwks", jwks)

	log.Printf("localoidc listening on %s (issuer %s)", addr, issuer)
	log.Printf("set on query-service: OIDC_ISSUER=%s OIDC_CLIENT_ID=%s OIDC_CLIENT_SECRET=anything", issuer, clientID)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/authorize",
		"token_endpoint":                        issuer + "/token",
		"jwks_uri":                              issuer + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid", "profile", "email"},
	})
}

var form = template.Must(template.New("f").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>Sign in (localoidc)</title>
<style>body{font-family:system-ui,sans-serif;max-width:26rem;margin:4rem auto;padding:0 1rem}
input,button{font:inherit;padding:.45rem .6rem;width:100%;margin:.3rem 0;box-sizing:border-box}
.hint{color:#666;font-size:.85rem}</style></head><body>
<h2>Local sign-in (dev/localoidc)</h2>
<p class="hint">Not a real identity provider. Whatever you type is who you become.</p>
<form method="post">
  <input type="hidden" name="state" value="{{.State}}">
  <input type="hidden" name="redirect_uri" value="{{.Redirect}}">
  <label>Email<input name="email" value="{{.Email}}" required></label>
  <label>Name<input name="name" value="{{.Name}}"></label>
  <button type="submit">Sign in</button>
</form>
</body></html>`))

func authorize(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		_ = form.Execute(w, struct{ State, Redirect, Email, Name string }{
			State:    q.Get("state"),
			Redirect: q.Get("redirect_uri"),
			Email:    "admin@local.test",
			Name:     "Local Admin",
		})
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id := identity{Email: r.FormValue("email"), Name: r.FormValue("name")}
	code := randomHex(16)

	mu.Lock()
	pending[code] = id
	mu.Unlock()

	redirect := r.FormValue("redirect_uri")
	u, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	q := u.Query()
	q.Set("code", code)
	q.Set("state", r.FormValue("state"))
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	code := r.FormValue("code")

	mu.Lock()
	id, ok := pending[code]
	delete(pending, code)
	mu.Unlock()
	if !ok {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}

	now := time.Now()
	claims := map[string]any{
		"iss":                issuer,
		"aud":                clientID,
		"sub":                shortHash(id.Email),
		"iat":                now.Unix(),
		"exp":                now.Add(time.Hour).Unix(),
		"email":              id.Email,
		"preferred_username": id.Email,
		"name":               id.Name,
	}
	idToken, err := signJWT(claims)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"access_token": "localoidc-access-token",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     idToken,
	})
}

func jwks(w http.ResponseWriter, _ *http.Request) {
	pub := key.Public().(*rsa.PublicKey)
	writeJSON(w, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
		"n": b64(pub.N.Bytes()),
		"e": b64(bigEndianExponent(pub.E)),
	}}})
}

// bigEndianExponent renders the public exponent minimally, as JWK requires.
func bigEndianExponent(e int) []byte {
	var out []byte
	for e > 0 {
		out = append([]byte{byte(e & 0xff)}, out...)
		e >>= 8
	}
	return out
}

func signJWT(claims map[string]any) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	payload, _ := json.Marshal(claims)
	signing := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + b64(sig), nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:8])
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
