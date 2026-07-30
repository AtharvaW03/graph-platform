package httpmw

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// KeyValidator answers whether a presented per-user key is currently active.
// internal/mcp.QueryClient.ValidateKey satisfies this.
type KeyValidator func(ctx context.Context, key string) (owner string, valid bool, err error)

// Cache TTLs. A positive hit is trusted for a minute, so a revoked or
// re-minted key can keep working for up to that long - the accepted price
// for not hitting the store on every request. Negatives are cached briefly
// too, so a misconfigured client retrying in a loop doesn't hammer the
// store, without locking a just-minted key out for long.
const (
	keyCachePositiveTTL = time.Minute
	keyCacheNegativeTTL = 10 * time.Second
	// keyCacheMaxEntries bounds the cache against a flood of random invalid
	// keys; on overflow the whole cache resets (cheap, and correct - it is
	// only a cache).
	keyCacheMaxEntries = 4096
)

// userKeyPrefix mirrors internal/keys.Prefix without importing the package
// (httpmw stays dependency-free of the domain packages it fronts).
const userKeyPrefix = "a1kg_"

type keyCacheEntry struct {
	owner string
	valid bool
	until time.Time
}

// WithUserKeyAuth wraps h with bearer authentication accepting EITHER the
// static shared token (constant-time compare, same as WithAuth) OR a
// portal-minted per-user key (prefix-recognized, checked through validate
// with a small TTL cache). /health and /ready stay unauthenticated.
//
// staticToken == "" disables the static path; validate == nil disables the
// per-user path; both disabled degrades to no auth (open mode), matching
// WithAuth's contract so callers' fail-closed checks stay in charge.
func WithUserKeyAuth(h http.Handler, staticToken string, validate KeyValidator) http.Handler {
	if staticToken == "" && validate == nil {
		return h
	}
	expected := []byte("Bearer " + staticToken)
	var (
		mu    sync.Mutex
		cache = make(map[[32]byte]keyCacheEntry)
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/ready" {
			h.ServeHTTP(w, r)
			return
		}
		got := r.Header.Get("Authorization")

		// Path 1: the static shared token.
		if staticToken != "" && subtle.ConstantTimeCompare([]byte(got), expected) == 1 {
			h.ServeHTTP(w, r)
			return
		}

		// Path 2: a per-user key.
		key, isBearer := strings.CutPrefix(got, "Bearer ")
		if validate != nil && isBearer && strings.HasPrefix(key, userKeyPrefix) {
			id := sha256.Sum256([]byte(key))
			now := time.Now()

			mu.Lock()
			entry, hit := cache[id]
			mu.Unlock()

			if !hit || now.After(entry.until) {
				owner, valid, err := validate(r.Context(), key)
				if err != nil {
					// Store unreachable: fail closed, but do not cache the
					// failure as "invalid" - the next request retries.
					log.Printf("user key validation failed: %v", err)
					writeErr(w, http.StatusServiceUnavailable, "authentication backend unavailable")
					return
				}
				entry = keyCacheEntry{owner: owner, valid: valid}
				if valid {
					entry.until = now.Add(keyCachePositiveTTL)
				} else {
					entry.until = now.Add(keyCacheNegativeTTL)
				}
				mu.Lock()
				if len(cache) >= keyCacheMaxEntries {
					cache = make(map[[32]byte]keyCacheEntry)
				}
				cache[id] = entry
				mu.Unlock()
			}

			if entry.valid {
				// Attribution for usage metrics: downstream handlers (and
				// the outbound query-service client) read this.
				h.ServeHTTP(w, r.WithContext(WithActor(r.Context(), entry.owner)))
				return
			}
		}

		writeErr(w, http.StatusUnauthorized, "missing or invalid bearer token")
	})
}
