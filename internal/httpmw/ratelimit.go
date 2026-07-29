package httpmw

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// WithPerActorRateLimit caps how many requests one identity may make per
// minute. This is fair-share protection, not DoS defense: the platform has
// a finite request budget, and one client looping at full speed must not
// consume all of it. Requests without an identity share a single bucket -
// they cannot be told apart, so they are limited together.
//
// The limiter is per-process and in-memory, which is exactly right for a
// single hosted mcp-server task. Running several would give each its own
// budget; that would need shared state, and is called out rather than
// silently assumed.
//
// perMinute <= 0 disables limiting entirely.
func WithPerActorRateLimit(h http.Handler, perMinute int) http.Handler {
	if perMinute <= 0 {
		return h
	}
	l := &actorLimiter{
		perMinute: perMinute,
		window:    time.Minute,
		buckets:   make(map[string]*bucket),
		now:       time.Now,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/ready" {
			h.ServeHTTP(w, r)
			return
		}
		actor := ActorFrom(r.Context())
		if actor == "" {
			actor = "-unattributed-"
		}
		allowed, retryAfter := l.allow(actor)
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			writeErr(w, http.StatusTooManyRequests,
				"rate limit exceeded: "+strconv.Itoa(perMinute)+" requests per minute per user")
			return
		}
		h.ServeHTTP(w, r)
	})
}

type bucket struct {
	count       int
	windowStart time.Time
}

type actorLimiter struct {
	perMinute int
	window    time.Duration
	now       func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	// lastSweep bounds map growth: buckets for actors who stopped calling
	// are dropped periodically rather than kept forever.
	lastSweep time.Time
}

func (l *actorLimiter) allow(actor string) (bool, time.Duration) {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSweep) > 10*l.window {
		for k, b := range l.buckets {
			if now.Sub(b.windowStart) > 2*l.window {
				delete(l.buckets, k)
			}
		}
		l.lastSweep = now
	}

	b, ok := l.buckets[actor]
	if !ok || now.Sub(b.windowStart) >= l.window {
		l.buckets[actor] = &bucket{count: 1, windowStart: now}
		return true, 0
	}
	if b.count >= l.perMinute {
		return false, l.window - now.Sub(b.windowStart)
	}
	b.count++
	return true, 0
}
