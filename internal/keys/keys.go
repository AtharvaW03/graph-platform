// Package keys implements per-user API keys for the hosted MCP endpoint:
// self-service minted (via the SSO portal), validated on every request, and
// expiring at the start of each calendar month (ILIP-style reset - engineers
// re-mint on the 1st, which forces a fresh SSO login and therefore re-proves
// the org account still exists; a disabled leaver's account can't renew).
//
// Keys are stored as (:ApiKey) nodes in Neo4j - deliberately NOT :Entity and
// carrying no repo, so the indexer's mark-and-sweep never touches them (same
// reasoning as :Feedback). Only the SHA-256 hash of a key is stored; the
// plaintext exists once, in the mint response.
package keys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"a1-knowledge-graph/internal/neo4j"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Prefix marks a per-user key on the wire, letting the MCP auth middleware
// distinguish "this is a portal-minted key, validate it against the store"
// from "this is (maybe) the static shared token" without a network call.
const Prefix = "a1kg_"

// rawBytes of entropy per key; 32 bytes = 256 bits, hex-encoded.
const rawBytes = 32

// txTimeout mirrors internal/query's transaction bound.
const txTimeout = 10 * time.Second

// Key is one stored key's metadata - never the secret itself.
type Key struct {
	ID         string     `json:"id"`
	Owner      string     `json:"owner"`      // email from the SSO ID token
	OwnerName  string     `json:"owner_name"` // display name from the SSO ID token
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// Active reports whether the key is currently usable.
func (k Key) Active(now time.Time) bool {
	return k.RevokedAt == nil && now.Before(k.ExpiresAt)
}

// Store persists and validates keys against Neo4j.
type Store struct {
	db *neo4j.Client
	// now is a hook for tests; defaults to time.Now.
	now func() time.Time
}

func NewStore(db *neo4j.Client) *Store {
	return &Store{db: db, now: time.Now}
}

// generate returns (plaintext, hash). The plaintext is shown to the user
// exactly once; only the hash is stored.
func generate() (string, string, error) {
	var b [rawBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("entropy: %w", err)
	}
	plaintext := Prefix + hex.EncodeToString(b[:])
	return plaintext, HashKey(plaintext), nil
}

// HashKey is the storage form of a key. SHA-256 is sufficient (keys are
// 256-bit random values, not passwords - no need for a slow hash).
func HashKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// IsUserKey reports whether a presented bearer credential is shaped like a
// portal-minted key (as opposed to the static shared token).
func IsUserKey(credential string) bool {
	return strings.HasPrefix(credential, Prefix)
}

// monthlyExpiry returns the ILIP-style reset boundary: the first instant of
// the month after now, in UTC. A key minted on Jan 3 and one minted on
// Jan 28 both expire at Feb 1 00:00 UTC.
func monthlyExpiry(now time.Time) time.Time {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
}

// Mint creates a new key for owner, revoking any previously active key
// (one active key per person - a re-mint is also the self-service "I think
// my key leaked" recovery). Returns the plaintext key, shown exactly once.
func (s *Store) Mint(ctx context.Context, owner, ownerName string) (string, Key, error) {
	owner = strings.ToLower(strings.TrimSpace(owner))
	if owner == "" {
		return "", Key{}, fmt.Errorf("owner required")
	}
	plaintext, hash, err := generate()
	if err != nil {
		return "", Key{}, err
	}
	now := s.now().UTC()
	k := Key{
		ID:        hash[:12], // stable public identifier, safe to display
		Owner:     owner,
		OwnerName: strings.TrimSpace(ownerName),
		CreatedAt: now,
		ExpiresAt: monthlyExpiry(now),
	}

	const cypher = `
MATCH (old:ApiKey {owner: $owner})
WHERE old.revoked_at IS NULL
SET old.revoked_at = datetime($now)
WITH count(old) AS _
CREATE (:ApiKey {
    id:         $id,
    hash:       $hash,
    owner:      $owner,
    owner_name: $owner_name,
    created_at: datetime($now),
    expires_at: datetime($expires)
})`
	err = s.write(ctx, cypher, map[string]any{
		"id":         k.ID,
		"hash":       hash,
		"owner":      k.Owner,
		"owner_name": k.OwnerName,
		"now":        now.Format(time.RFC3339),
		"expires":    k.ExpiresAt.Format(time.RFC3339),
	})
	if err != nil {
		return "", Key{}, err
	}
	return plaintext, k, nil
}

// Validate checks a presented plaintext key and returns its owner when the
// key exists, is unrevoked, and is unexpired. The stored hash is compared
// constant-time (defense in depth; the lookup is already by exact hash).
// A valid hit also stamps last_used_at, best-effort.
func (s *Store) Validate(ctx context.Context, plaintext string) (Key, bool, error) {
	hash := HashKey(plaintext)
	const cypher = `
MATCH (k:ApiKey {hash: $hash})
WHERE k.revoked_at IS NULL AND k.expires_at > datetime($now)
SET k.last_used_at = datetime($now)
RETURN k.id AS id, k.hash AS hash, k.owner AS owner, k.owner_name AS owner_name,
       k.created_at AS created_at, k.expires_at AS expires_at
LIMIT 1`
	now := s.now().UTC()
	var out Key
	found := false
	err := s.writeRows(ctx, cypher, map[string]any{"hash": hash, "now": now.Format(time.RFC3339)}, func(m map[string]any) {
		stored, _ := m["hash"].(string)
		if subtle.ConstantTimeCompare([]byte(stored), []byte(hash)) != 1 {
			return
		}
		out = Key{
			ID:        asString(m["id"]),
			Owner:     asString(m["owner"]),
			OwnerName: asString(m["owner_name"]),
			CreatedAt: asTime(m["created_at"]),
			ExpiresAt: asTime(m["expires_at"]),
		}
		found = true
	})
	if err != nil {
		return Key{}, false, err
	}
	return out, found, nil
}

// ActiveKey returns owner's currently active key metadata, if any.
func (s *Store) ActiveKey(ctx context.Context, owner string) (Key, bool, error) {
	owner = strings.ToLower(strings.TrimSpace(owner))
	const cypher = `
MATCH (k:ApiKey {owner: $owner})
WHERE k.revoked_at IS NULL AND k.expires_at > datetime($now)
RETURN k.id AS id, k.owner AS owner, k.owner_name AS owner_name,
       k.created_at AS created_at, k.expires_at AS expires_at,
       k.last_used_at AS last_used_at
LIMIT 1`
	var out Key
	found := false
	err := s.readRows(ctx, cypher, map[string]any{"owner": owner, "now": s.now().UTC().Format(time.RFC3339)}, func(m map[string]any) {
		out = Key{
			ID:        asString(m["id"]),
			Owner:     asString(m["owner"]),
			OwnerName: asString(m["owner_name"]),
			CreatedAt: asTime(m["created_at"]),
			ExpiresAt: asTime(m["expires_at"]),
		}
		if t := asTime(m["last_used_at"]); !t.IsZero() {
			out.LastUsedAt = &t
		}
		found = true
	})
	return out, found, err
}

// RevokeOwn revokes owner's active key (self-service). Returns whether a
// key was actually revoked.
func (s *Store) RevokeOwn(ctx context.Context, owner string) (bool, error) {
	owner = strings.ToLower(strings.TrimSpace(owner))
	const cypher = `
MATCH (k:ApiKey {owner: $owner})
WHERE k.revoked_at IS NULL
SET k.revoked_at = datetime($now)
RETURN count(k) AS n`
	n := 0
	err := s.writeRows(ctx, cypher, map[string]any{"owner": owner, "now": s.now().UTC().Format(time.RFC3339)}, func(m map[string]any) {
		n = asInt(m["n"])
	})
	return n > 0, err
}

// List returns every key, newest first - the admin oversight view.
func (s *Store) List(ctx context.Context) ([]Key, error) {
	const cypher = `
MATCH (k:ApiKey)
RETURN k.id AS id, k.owner AS owner, k.owner_name AS owner_name,
       k.created_at AS created_at, k.expires_at AS expires_at,
       k.revoked_at AS revoked_at, k.last_used_at AS last_used_at
ORDER BY created_at DESC`
	out := make([]Key, 0, 16)
	err := s.readRows(ctx, cypher, nil, func(m map[string]any) {
		k := Key{
			ID:        asString(m["id"]),
			Owner:     asString(m["owner"]),
			OwnerName: asString(m["owner_name"]),
			CreatedAt: asTime(m["created_at"]),
			ExpiresAt: asTime(m["expires_at"]),
		}
		if t := asTime(m["revoked_at"]); !t.IsZero() {
			k.RevokedAt = &t
		}
		if t := asTime(m["last_used_at"]); !t.IsZero() {
			k.LastUsedAt = &t
		}
		out = append(out, k)
	})
	return out, err
}

// -------- Neo4j plumbing --------

func (s *Store) write(ctx context.Context, cypher string, params map[string]any) error {
	return s.writeRows(ctx, cypher, params, nil)
}

func (s *Store) writeRows(ctx context.Context, cypher string, params map[string]any, fn func(map[string]any)) error {
	sess := s.db.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeWrite})
	defer sess.Close(ctx)
	_, err := sess.ExecuteWrite(ctx, func(tx driver.ManagedTransaction) (any, error) {
		return runRows(ctx, tx, cypher, params, fn)
	}, driver.WithTxTimeout(txTimeout))
	return err
}

func (s *Store) readRows(ctx context.Context, cypher string, params map[string]any, fn func(map[string]any)) error {
	sess := s.db.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeRead})
	defer sess.Close(ctx)
	_, err := sess.ExecuteRead(ctx, func(tx driver.ManagedTransaction) (any, error) {
		return runRows(ctx, tx, cypher, params, fn)
	}, driver.WithTxTimeout(txTimeout))
	return err
}

func runRows(ctx context.Context, tx driver.ManagedTransaction, cypher string, params map[string]any, fn func(map[string]any)) (any, error) {
	res, err := tx.Run(ctx, cypher, params)
	if err != nil {
		return nil, err
	}
	records, err := res.Collect(ctx)
	if err != nil {
		return nil, err
	}
	if fn != nil {
		for _, r := range records {
			fn(r.AsMap())
		}
	}
	return nil, nil
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asInt(v any) int {
	if i, ok := v.(int64); ok {
		return int(i)
	}
	return 0
}

func asTime(v any) time.Time {
	if t, ok := v.(time.Time); ok {
		return t.UTC()
	}
	return time.Time{}
}
