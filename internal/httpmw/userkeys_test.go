package httpmw

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// okHandler is shared with auth_test.go.

func doReq(t *testing.T, h http.Handler, path, auth string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestWithUserKeyAuth_StaticToken(t *testing.T) {
	h := WithUserKeyAuth(okHandler(), "shared-secret", nil)

	if got := doReq(t, h, "/mcp", "Bearer shared-secret"); got != http.StatusOK {
		t.Errorf("static token: %d, want 200", got)
	}
	if got := doReq(t, h, "/mcp", "Bearer wrong"); got != http.StatusUnauthorized {
		t.Errorf("wrong token: %d, want 401", got)
	}
	if got := doReq(t, h, "/mcp", ""); got != http.StatusUnauthorized {
		t.Errorf("no token: %d, want 401", got)
	}
	if got := doReq(t, h, "/health", ""); got != http.StatusOK {
		t.Errorf("health exempt: %d, want 200", got)
	}
}

func TestWithUserKeyAuth_UserKey(t *testing.T) {
	var calls atomic.Int32
	validate := func(_ context.Context, key string) (string, bool, error) {
		calls.Add(1)
		return "alice@example.com", key == "a1kg_goodkey", nil
	}
	h := WithUserKeyAuth(okHandler(), "shared-secret", validate)

	if got := doReq(t, h, "/mcp", "Bearer a1kg_goodkey"); got != http.StatusOK {
		t.Errorf("valid user key: %d, want 200", got)
	}
	if got := doReq(t, h, "/mcp", "Bearer a1kg_badkey"); got != http.StatusUnauthorized {
		t.Errorf("invalid user key: %d, want 401", got)
	}
	// A non-prefixed credential must not reach the validator.
	before := calls.Load()
	if got := doReq(t, h, "/mcp", "Bearer random-string"); got != http.StatusUnauthorized {
		t.Errorf("non-key credential: %d, want 401", got)
	}
	if calls.Load() != before {
		t.Error("validator called for a non-prefixed credential")
	}
	// Static token still works alongside the key path.
	if got := doReq(t, h, "/mcp", "Bearer shared-secret"); got != http.StatusOK {
		t.Errorf("static token with keys enabled: %d, want 200", got)
	}
}

func TestWithUserKeyAuth_CachesPositive(t *testing.T) {
	var calls atomic.Int32
	validate := func(_ context.Context, _ string) (string, bool, error) {
		calls.Add(1)
		return "bob@example.com", true, nil
	}
	h := WithUserKeyAuth(okHandler(), "", validate)

	for i := 0; i < 5; i++ {
		if got := doReq(t, h, "/mcp", "Bearer a1kg_cachedkey"); got != http.StatusOK {
			t.Fatalf("request %d: %d, want 200", i, got)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("validator called %d times for 5 requests, want 1 (cache)", calls.Load())
	}
}

func TestWithUserKeyAuth_BackendDownFailsClosed(t *testing.T) {
	validate := func(_ context.Context, _ string) (string, bool, error) {
		return "", false, errors.New("connection refused")
	}
	h := WithUserKeyAuth(okHandler(), "", validate)

	if got := doReq(t, h, "/mcp", "Bearer a1kg_anykey"); got != http.StatusServiceUnavailable {
		t.Errorf("backend down: %d, want 503 (fail closed, retryable)", got)
	}
}

func TestWithUserKeyAuth_BothDisabledIsOpen(t *testing.T) {
	h := WithUserKeyAuth(okHandler(), "", nil)
	if got := doReq(t, h, "/mcp", ""); got != http.StatusOK {
		t.Errorf("open mode: %d, want 200", got)
	}
}
