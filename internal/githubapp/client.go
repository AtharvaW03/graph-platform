// Package githubapp mints GitHub App JWTs and exchanges them for
// installation access tokens, so the indexer can authenticate against
// private repositories without a long-lived personal access token.
package githubapp

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// tokenExpiryBuffer is how far ahead of a token's real expiry
// InstallationToken treats it as stale and mints a replacement. Wide enough
// to cover the gap between the cache check and the token actually being used
// (a graphify run, a git push), not just the token-exchange round trip.
const tokenExpiryBuffer = 5 * time.Minute

// Client authenticates as a single GitHub App installation. Safe for
// concurrent use: InstallationToken caches the minted token behind a mutex
// and only re-mints once the cached one is within tokenExpiryBuffer of
// expiring.
type Client struct {
	AppID          string
	InstallationID string
	PrivateKey     *rsa.PrivateKey
	HTTP           *http.Client
	APIBase        string

	mu    sync.Mutex
	token string
	exp   time.Time
}

// New parses a PEM-encoded RSA private key (downloaded once from the App's
// settings page on GitHub) and builds a Client for the given installation.
func New(appID, installationID string, pemKey []byte) (*Client, error) {
	key, err := jwt.ParseRSAPrivateKeyFromPEM(pemKey)
	if err != nil {
		return nil, fmt.Errorf("parse github app private key: %w", err)
	}
	return &Client{
		AppID:          appID,
		InstallationID: installationID,
		PrivateKey:     key,
		// Bounded client: a stalled connection to GitHub must not hang
		// indexer startup or wedge the background refresher forever.
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		APIBase: "https://api.github.com",
	}, nil
}

// AppJWT returns a short-lived JWT identifying the App itself, the only
// credential GitHub accepts for minting an installation access token.
// Backdated 60s to tolerate clock skew against GitHub's servers; GitHub caps
// App JWT lifetime at 10 minutes, so 9 leaves margin either side.
func (c *Client) AppJWT() (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": c.AppID,
	}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return t.SignedString(c.PrivateKey)
}

// InstallationToken returns a cached installation access token, minting a
// fresh one if none is cached or the cached one is within tokenExpiryBuffer
// of expiring. GitHub installation tokens are valid for 1 hour.
func (c *Client) InstallationToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.exp.Add(-tokenExpiryBuffer)) {
		return c.token, nil
	}
	return c.mintLocked(ctx)
}

// Refresh mints a new installation token unconditionally, bypassing the
// cache. Callers that persist the token somewhere with its own lifetime
// (the git credential store) must use this: a periodic refresher going
// through InstallationToken would get the cached token back whenever it
// fires earlier than tokenExpiryBuffer before expiry, and would then store
// a credential that dies long before the next tick.
func (c *Client) Refresh(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mintLocked(ctx)
}

// mintLocked exchanges an App JWT for a fresh installation token and caches
// it. Caller must hold c.mu.
func (c *Client) mintLocked(ctx context.Context) (string, error) {
	jwtStr, err := c.AppJWT()
	if err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("%s/app/installations/%s/access_tokens", c.APIBase, c.InstallationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwtStr)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("installation token request: %w", err)
	}
	defer resp.Body.Close()
	// The token response is a few hundred bytes; 1MB caps a misbehaving
	// endpoint from ballooning memory.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("installation token: %s: %s", resp.Status, body)
	}

	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("parse installation token response: %w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("installation token response missing token field")
	}
	c.token = out.Token
	c.exp = out.ExpiresAt
	if c.exp.IsZero() {
		// GitHub always sends expires_at in practice; this guards only
		// against caching an empty exp that would never look stale.
		c.exp = time.Now().Add(50 * time.Minute)
	}
	return c.token, nil
}

// AuthenticatedCloneURL rewrites a GitHub HTTPS clone URL to carry token as
// the x-access-token basic-auth user, the form git and GitHub's own tooling
// expect for App-authenticated clones and pushes.
func AuthenticatedCloneURL(rawURL, token string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse clone url: %w", err)
	}
	u.User = url.UserPassword("x-access-token", token)
	return u.String(), nil
}
