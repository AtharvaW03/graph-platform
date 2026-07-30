package httpmw

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func reqAs(t *testing.T, h http.Handler, actor, path string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if actor != "" {
		req = req.WithContext(WithActor(req.Context(), actor))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestRateLimit_CapsOneActor(t *testing.T) {
	h := WithPerActorRateLimit(okHandler(), 3)

	for i := 1; i <= 3; i++ {
		if got := reqAs(t, h, "alice@org", "/mcp"); got != http.StatusOK {
			t.Fatalf("request %d: %d, want 200", i, got)
		}
	}
	if got := reqAs(t, h, "alice@org", "/mcp"); got != http.StatusTooManyRequests {
		t.Errorf("request 4: %d, want 429", got)
	}
}

// The whole point of per-actor limiting: one heavy user must not consume
// everyone else's budget.
func TestRateLimit_IsolatesActors(t *testing.T) {
	h := WithPerActorRateLimit(okHandler(), 2)

	reqAs(t, h, "heavy@org", "/mcp")
	reqAs(t, h, "heavy@org", "/mcp")
	if got := reqAs(t, h, "heavy@org", "/mcp"); got != http.StatusTooManyRequests {
		t.Fatalf("heavy user not limited: %d", got)
	}
	if got := reqAs(t, h, "light@org", "/mcp"); got != http.StatusOK {
		t.Errorf("second user blocked by the first user's usage: %d, want 200", got)
	}
}

func TestRateLimit_WindowResets(t *testing.T) {
	h := WithPerActorRateLimit(okHandler(), 1)
	// Reach into the limiter's clock through a fresh instance.
	l := &actorLimiter{perMinute: 1, window: time.Minute, buckets: map[string]*bucket{}}
	base := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return base }

	if ok, _ := l.allow("a"); !ok {
		t.Fatal("first request denied")
	}
	if ok, _ := l.allow("a"); ok {
		t.Fatal("second request in the same window allowed")
	}
	l.now = func() time.Time { return base.Add(61 * time.Second) }
	if ok, _ := l.allow("a"); !ok {
		t.Error("request in the next window denied - the window never reset")
	}
	_ = h
}

func TestRateLimit_HealthExempt(t *testing.T) {
	h := WithPerActorRateLimit(okHandler(), 1)
	reqAs(t, h, "probe", "/health")
	for i := 0; i < 5; i++ {
		if got := reqAs(t, h, "probe", "/health"); got != http.StatusOK {
			t.Fatalf("health probe rate-limited: %d", got)
		}
	}
}

func TestRateLimit_SendsRetryAfter(t *testing.T) {
	h := WithPerActorRateLimit(okHandler(), 1)
	reqAs(t, h, "alice@org", "/mcp")

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req = req.WithContext(WithActor(req.Context(), "alice@org"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 without a Retry-After header - clients cannot back off correctly")
	}
}

func TestRateLimit_DisabledWhenZero(t *testing.T) {
	h := WithPerActorRateLimit(okHandler(), 0)
	for i := 0; i < 50; i++ {
		if got := reqAs(t, h, "alice@org", "/mcp"); got != http.StatusOK {
			t.Fatalf("limiting active when disabled: %d", got)
		}
	}
}
