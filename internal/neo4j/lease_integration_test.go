package neo4j

import (
	"context"
	"errors"
	"testing"
	"time"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// resetLease clears the singleton lease row so each test starts from a clean
// slate. The lease is a true singleton (one row, id:"singleton"), so these
// tests must not run in parallel with each other - none of them call
// t.Parallel(), matching Go's default sequential-within-file execution.
func resetLease(t *testing.T, c *Client) {
	t.Helper()
	ctx := context.Background()
	session := c.Driver.NewSession(ctx, driver.SessionConfig{})
	defer session.Close(ctx)
	if _, err := session.Run(ctx, `MATCH (l:`+leaseLabel+` {id:'singleton'}) DELETE l`, nil); err != nil {
		t.Fatalf("reset lease: %v", err)
	}
}

func TestIntegration_AcquireLease_Ok(t *testing.T) {
	c := testClient(t)
	resetLease(t, c)
	t.Cleanup(func() { resetLease(t, c) })

	if err := c.AcquireLease(context.Background(), "owner-a", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// Re-acquiring as the same owner is idempotent, not a conflict.
	if err := c.AcquireLease(context.Background(), "owner-a", time.Minute); err != nil {
		t.Fatalf("re-acquire by same owner: %v", err)
	}
}

func TestIntegration_AcquireLease_RefusedWhileHeld(t *testing.T) {
	c := testClient(t)
	resetLease(t, c)
	t.Cleanup(func() { resetLease(t, c) })
	ctx := context.Background()

	if err := c.AcquireLease(ctx, "owner-a", time.Minute); err != nil {
		t.Fatalf("acquire by owner-a: %v", err)
	}

	err := c.AcquireLease(ctx, "owner-b", time.Minute)
	if err == nil {
		t.Fatal("expected owner-b to be refused while owner-a's lease is unexpired")
	}
	var held *ErrLeaseHeld
	if !errors.As(err, &held) {
		t.Fatalf("expected *ErrLeaseHeld, got %T: %v", err, err)
	}
	if held.Owner != "owner-a" {
		t.Errorf("ErrLeaseHeld.Owner = %q, want owner-a", held.Owner)
	}
}

func TestIntegration_AcquireLease_SucceedsAfterExpiry(t *testing.T) {
	c := testClient(t)
	resetLease(t, c)
	t.Cleanup(func() { resetLease(t, c) })
	ctx := context.Background()

	if err := c.AcquireLease(ctx, "owner-a", 150*time.Millisecond); err != nil {
		t.Fatalf("acquire by owner-a: %v", err)
	}
	time.Sleep(400 * time.Millisecond)

	if err := c.AcquireLease(ctx, "owner-b", time.Minute); err != nil {
		t.Fatalf("owner-b should succeed once owner-a's lease expired: %v", err)
	}
}

func TestIntegration_RenewLease_AfterStealFails(t *testing.T) {
	c := testClient(t)
	resetLease(t, c)
	t.Cleanup(func() { resetLease(t, c) })
	ctx := context.Background()

	if err := c.AcquireLease(ctx, "owner-a", time.Minute); err != nil {
		t.Fatalf("acquire by owner-a: %v", err)
	}
	if err := c.StealLease(ctx, "owner-b", time.Minute); err != nil {
		t.Fatalf("steal by owner-b: %v", err)
	}

	err := c.RenewLease(ctx, "owner-a", time.Minute)
	if err == nil {
		t.Fatal("expected owner-a's renewal to fail after owner-b stole the lease")
	}
	if !errors.Is(err, ErrLeaseLost) {
		t.Errorf("expected ErrLeaseLost, got %v", err)
	}
}

func TestIntegration_ReleaseLease_ThenReacquire(t *testing.T) {
	c := testClient(t)
	resetLease(t, c)
	t.Cleanup(func() { resetLease(t, c) })
	ctx := context.Background()

	if err := c.AcquireLease(ctx, "owner-a", time.Minute); err != nil {
		t.Fatalf("acquire by owner-a: %v", err)
	}
	if err := c.ReleaseLease(ctx, "owner-a"); err != nil {
		t.Fatalf("release by owner-a: %v", err)
	}
	if err := c.AcquireLease(ctx, "owner-b", time.Minute); err != nil {
		t.Fatalf("owner-b should acquire freely after release: %v", err)
	}
}
