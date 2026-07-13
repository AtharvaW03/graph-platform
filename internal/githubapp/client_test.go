package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// testKey generates a throwaway RSA key and its PEM encoding for use in a
// single test. 2048 bits is the minimum GitHub accepts and keeps keygen fast.
func testKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test rsa key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	return key, pem.EncodeToMemory(block)
}

func TestNew_ParsesValidPEM(t *testing.T) {
	_, pemBytes := testKey(t)
	c, err := New("app-1", "install-1", pemBytes)
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	if c.AppID != "app-1" || c.InstallationID != "install-1" {
		t.Errorf("New() = %+v, want AppID=app-1 InstallationID=install-1", c)
	}
	if c.APIBase != "https://api.github.com" {
		t.Errorf("APIBase = %q, want the default GitHub API base", c.APIBase)
	}
}

func TestNew_RejectsInvalidPEM(t *testing.T) {
	if _, err := New("app-1", "install-1", []byte("not a pem")); err == nil {
		t.Error("New() with garbage PEM bytes = nil error, want an error")
	}
}

func TestAppJWT_ClaimsShape(t *testing.T) {
	key, pemBytes := testKey(t)
	c, err := New("my-app-id", "install-1", pemBytes)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	before := time.Now()
	tokenStr, err := c.AppJWT()
	if err != nil {
		t.Fatalf("AppJWT() error = %v", err)
	}

	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Method)
		}
		return &key.PublicKey, nil
	})
	if err != nil {
		t.Fatalf("parse minted JWT: %v", err)
	}
	if !parsed.Valid {
		t.Fatal("minted JWT did not validate against its own public key")
	}
	if parsed.Method.Alg() != "RS256" {
		t.Errorf("alg = %s, want RS256", parsed.Method.Alg())
	}

	iss, _ := claims.GetIssuer()
	if iss != "my-app-id" {
		t.Errorf("iss = %q, want %q", iss, "my-app-id")
	}

	iat, err := claims.GetIssuedAt()
	if err != nil || iat == nil {
		t.Fatalf("GetIssuedAt() = %v, %v", iat, err)
	}
	if skew := before.Sub(iat.Time); skew < 55*time.Second || skew > 65*time.Second {
		t.Errorf("iat is %s before now, want ~60s (clock-skew backdating)", skew)
	}

	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil {
		t.Fatalf("GetExpirationTime() = %v, %v", exp, err)
	}
	if ttl := exp.Sub(before); ttl < 8*time.Minute || ttl > 10*time.Minute {
		t.Errorf("exp is %s from now, want ~9m (under GitHub's 10m App-JWT cap)", ttl)
	}
}

// tokenServer returns an httptest.Server standing in for api.github.com's
// installation-token endpoint, plus a counter of how many times it was hit.
// verifyJWT is called with the bearer token from each request's Authorization
// header so callers can assert it's a well-formed App JWT.
func tokenServer(t *testing.T, token string, expiresAt time.Time, verifyJWT func(t *testing.T, bearer string)) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Method != http.MethodPost {
			t.Errorf("request method = %s, want POST", r.Method)
		}
		wantPath := "/app/installations/install-1/access_tokens"
		if r.URL.Path != wantPath {
			t.Errorf("request path = %s, want %s", r.URL.Path, wantPath)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept header = %q, want the GitHub v3 media type", got)
		}
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if bearer == r.Header.Get("Authorization") {
			t.Error("Authorization header missing the Bearer prefix")
		}
		if verifyJWT != nil {
			verifyJWT(t, bearer)
		}

		w.Header().Set("Content-Type", "application/json")
		body := fmt.Sprintf(`{"token":%q`, token)
		if !expiresAt.IsZero() {
			body += fmt.Sprintf(`,"expires_at":%q`, expiresAt.Format(time.RFC3339))
		}
		body += "}"
		w.Write([]byte(body))
	}))
	return srv, &calls
}

func TestInstallationToken_RequestShapeAndCaching(t *testing.T) {
	key, pemBytes := testKey(t)
	c, err := New("app-1", "install-1", pemBytes)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	srv, calls := tokenServer(t, "tok-abc123", time.Now().Add(1*time.Hour), func(t *testing.T, bearer string) {
		if _, err := jwt.Parse(bearer, func(tok *jwt.Token) (interface{}, error) {
			return &key.PublicKey, nil
		}); err != nil {
			t.Errorf("Authorization bearer did not parse as a JWT signed by the client's key: %v", err)
		}
	})
	defer srv.Close()
	c.APIBase = srv.URL

	got, err := c.InstallationToken(context.Background())
	if err != nil {
		t.Fatalf("InstallationToken() error = %v", err)
	}
	if got != "tok-abc123" {
		t.Errorf("InstallationToken() = %q, want %q", got, "tok-abc123")
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("server hit %d times minting the first token, want 1", n)
	}

	// A far-future expiry means the cache is fresh - a second call must not
	// hit the server again.
	got2, err := c.InstallationToken(context.Background())
	if err != nil {
		t.Fatalf("second InstallationToken() error = %v", err)
	}
	if got2 != got {
		t.Errorf("second InstallationToken() = %q, want the cached %q", got2, got)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Errorf("server hit %d times after a cached call, want still 1 (no re-mint)", n)
	}
}

func TestInstallationToken_RefreshesWithinExpiryBuffer(t *testing.T) {
	_, pemBytes := testKey(t)
	c, err := New("app-1", "install-1", pemBytes)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Expires in 4 minutes - inside the 5-minute tokenExpiryBuffer, so the
	// very first call already caches a token InstallationToken must treat as
	// stale on the next check.
	srv, calls := tokenServer(t, "tok-near-expiry", time.Now().Add(4*time.Minute), nil)
	defer srv.Close()
	c.APIBase = srv.URL

	if _, err := c.InstallationToken(context.Background()); err != nil {
		t.Fatalf("first InstallationToken() error = %v", err)
	}
	if _, err := c.InstallationToken(context.Background()); err != nil {
		t.Fatalf("second InstallationToken() error = %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Errorf("server hit %d times, want 2 - a token inside the expiry buffer must be re-minted", n)
	}
}

func TestInstallationToken_ZeroExpiresAtDefaultsToFarFuture(t *testing.T) {
	_, pemBytes := testKey(t)
	c, err := New("app-1", "install-1", pemBytes)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// No expires_at in the response at all - the client must not treat that
	// as "already expired" and re-mint on every call.
	srv, calls := tokenServer(t, "tok-no-expiry", time.Time{}, nil)
	defer srv.Close()
	c.APIBase = srv.URL

	if _, err := c.InstallationToken(context.Background()); err != nil {
		t.Fatalf("first InstallationToken() error = %v", err)
	}
	if _, err := c.InstallationToken(context.Background()); err != nil {
		t.Fatalf("second InstallationToken() error = %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Errorf("server hit %d times, want 1 - a missing expires_at must still be cached with a sane default", n)
	}
}

// TestRefresh_BypassesFreshCache is the credential-store scenario: the
// periodic refresher fires while the cached token is still comfortably fresh
// (minute 50 of 60). Going through InstallationToken there would return the
// old token and the credential file would expire before the next tick;
// Refresh must mint regardless of cache state.
func TestRefresh_BypassesFreshCache(t *testing.T) {
	_, pemBytes := testKey(t)
	c, err := New("app-1", "install-1", pemBytes)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	srv, calls := tokenServer(t, "tok-fresh", time.Now().Add(1*time.Hour), nil)
	defer srv.Close()
	c.APIBase = srv.URL

	if _, err := c.InstallationToken(context.Background()); err != nil {
		t.Fatalf("InstallationToken() error = %v", err)
	}
	if _, err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Fatalf("server hit %d times, want 2 - Refresh must mint even with a fresh cached token", n)
	}

	// And the fresh mint must now be the cached token for ordinary callers.
	if _, err := c.InstallationToken(context.Background()); err != nil {
		t.Fatalf("InstallationToken() after Refresh error = %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Errorf("server hit %d times, want still 2 - Refresh result should be cached", n)
	}
}

func TestInstallationToken_MissingTokenFieldErrors(t *testing.T) {
	_, pemBytes := testKey(t)
	c, err := New("app-1", "install-1", pemBytes)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"expires_at":"2030-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()
	c.APIBase = srv.URL

	if _, err := c.InstallationToken(context.Background()); err == nil {
		t.Fatal("InstallationToken() with no token in the response = nil error, want an error")
	}
}

func TestInstallationToken_ErrorStatusPropagates(t *testing.T) {
	_, pemBytes := testKey(t)
	c, err := New("app-1", "install-1", pemBytes)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()
	c.APIBase = srv.URL

	_, err = c.InstallationToken(context.Background())
	if err == nil {
		t.Fatal("InstallationToken() with a 401 response = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %v, want it to mention the 401 status", err)
	}
}

func TestAuthenticatedCloneURL(t *testing.T) {
	got, err := AuthenticatedCloneURL("https://github.com/org/repo.git", "tok-xyz")
	if err != nil {
		t.Fatalf("AuthenticatedCloneURL() error = %v", err)
	}
	want := "https://x-access-token:tok-xyz@github.com/org/repo.git"
	if got != want {
		t.Errorf("AuthenticatedCloneURL() = %q, want %q", got, want)
	}
}

func TestAuthenticatedCloneURL_InvalidURL(t *testing.T) {
	if _, err := AuthenticatedCloneURL("://not a url", "tok-xyz"); err == nil {
		t.Error("AuthenticatedCloneURL() with an unparseable URL = nil error, want an error")
	}
}
