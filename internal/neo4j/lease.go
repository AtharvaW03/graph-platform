package neo4j

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// LeaseOwner builds a writer-lease identity for the calling process:
// hostname and pid so an operator can tell at a glance which machine/process
// holds a lease, plus a short random suffix so two processes racing to start
// on the same host don't collide on identity. Shared by cmd/indexer and
// cmd/importer so both build owner strings the same way.
func LeaseOwner() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	var b [4]byte
	suffix := "????"
	if _, err := rand.Read(b[:]); err == nil {
		suffix = hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), suffix)
}

// leaseLabel is deliberately not :Entity - the lease row must be invisible to
// every Entity-scoped query (sweep, count, search) so it never gets deleted
// by a sweep or shows up in a query result. Every query elsewhere in this
// codebase matches on an explicit label, so an IndexerLease node is never
// picked up by an unlabeled MATCH.
const leaseLabel = "IndexerLease"

// ErrLeaseHeld is returned by AcquireLease when another owner holds an
// unexpired lease.
type ErrLeaseHeld struct {
	Owner   string
	Expires time.Time
}

func (e *ErrLeaseHeld) Error() string {
	return fmt.Sprintf("writer lease held by %q until %s", e.Owner, e.Expires.Format(time.RFC3339))
}

// ErrLeaseLost is returned by RenewLease when the caller no longer owns the
// lease - someone else claimed it (an expiry, or --steal-lease).
var ErrLeaseLost = errors.New("writer lease lost")

// acquireLeaseQuery MERGEs the singleton lease row and claims it in one
// atomic write: claimable when nobody holds it yet, the current holder's
// lease already expired, or the caller already holds it (idempotent
// re-acquire/renew-by-acquire). All time math uses Neo4j's server clock
// (timestamp(), epoch millis) so host clock skew between indexer machines
// never matters. The FOREACH/CASE trick applies the claim conditionally
// without a WITH...WHERE, which would otherwise make a refused claim look
// like "no row returned" instead of "here's who holds it".
const acquireLeaseQuery = `
MERGE (l:` + leaseLabel + ` {id: 'singleton'})
ON CREATE SET l.owner = $owner, l.acquired_at = timestamp(), l.expires_at = timestamp() + $ttlMs
WITH l, (l.owner = $owner OR l.expires_at < timestamp()) AS claimable
FOREACH (_ IN CASE WHEN claimable THEN [1] ELSE [] END |
  SET l.owner = $owner, l.acquired_at = timestamp(), l.expires_at = timestamp() + $ttlMs
)
RETURN l.owner AS owner, l.expires_at AS expires_at`

// AcquireLease claims the singleton writer lease for owner. Succeeds if the
// lease is unclaimed, expired, or already held by owner; fails with
// *ErrLeaseHeld naming the current holder otherwise.
func (c *Client) AcquireLease(ctx context.Context, owner string, ttl time.Duration) error {
	session := c.Driver.NewSession(ctx, driver.SessionConfig{})
	defer session.Close(ctx)

	res, err := session.Run(ctx, acquireLeaseQuery, map[string]any{"owner": owner, "ttlMs": ttl.Milliseconds()})
	if err != nil {
		return fmt.Errorf("acquire lease: %w", err)
	}
	rec, err := res.Single(ctx)
	if err != nil {
		return fmt.Errorf("acquire lease (read): %w", err)
	}
	m := rec.AsMap()
	gotOwner, _ := m["owner"].(string)
	if gotOwner != owner {
		expiresMs, _ := m["expires_at"].(int64)
		return &ErrLeaseHeld{Owner: gotOwner, Expires: time.UnixMilli(expiresMs)}
	}
	return nil
}

// StealLease claims the lease unconditionally, regardless of current owner or
// expiry. It's the operator recovery path (--steal-lease) for a stuck lease
// left behind by a crashed indexer that hasn't hit its TTL yet.
func (c *Client) StealLease(ctx context.Context, owner string, ttl time.Duration) error {
	session := c.Driver.NewSession(ctx, driver.SessionConfig{})
	defer session.Close(ctx)

	query := `
MERGE (l:` + leaseLabel + ` {id: 'singleton'})
SET l.owner = $owner, l.acquired_at = timestamp(), l.expires_at = timestamp() + $ttlMs`
	_, err := session.Run(ctx, query, map[string]any{"owner": owner, "ttlMs": ttl.Milliseconds()})
	if err != nil {
		return fmt.Errorf("steal lease: %w", err)
	}
	return nil
}

// RenewLease extends the lease's expiry, but only while owner still holds it.
// Returns ErrLeaseLost if owner no longer owns the lease (expired and claimed
// by someone else, or stolen) - the caller must treat that as fatal and stop
// writing immediately.
func (c *Client) RenewLease(ctx context.Context, owner string, ttl time.Duration) error {
	session := c.Driver.NewSession(ctx, driver.SessionConfig{})
	defer session.Close(ctx)

	query := `
MATCH (l:` + leaseLabel + ` {id: 'singleton'})
WHERE l.owner = $owner
SET l.expires_at = timestamp() + $ttlMs
RETURN l.owner AS owner`
	res, err := session.Run(ctx, query, map[string]any{"owner": owner, "ttlMs": ttl.Milliseconds()})
	if err != nil {
		return fmt.Errorf("renew lease: %w", err)
	}
	if _, err := res.Single(ctx); err != nil {
		return fmt.Errorf("renew lease for %q: %w", owner, ErrLeaseLost)
	}
	return nil
}

// ReleaseLease deletes the lease row, but only if owner still holds it - a
// process that already lost the lease must not delete whoever holds it now.
// No error when owner doesn't hold it; release is a best-effort cleanup on
// shutdown, not a contested operation.
func (c *Client) ReleaseLease(ctx context.Context, owner string) error {
	session := c.Driver.NewSession(ctx, driver.SessionConfig{})
	defer session.Close(ctx)

	query := `
MATCH (l:` + leaseLabel + ` {id: 'singleton'})
WHERE l.owner = $owner
DELETE l`
	_, err := session.Run(ctx, query, map[string]any{"owner": owner})
	if err != nil {
		return fmt.Errorf("release lease: %w", err)
	}
	return nil
}
