package keys

import (
	"context"
	"os"
	"testing"
	"time"

	"a1-knowledge-graph/internal/neo4j"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Integration tests: run only against a real Neo4j (same convention as
// internal/neo4j and internal/query - NEO4J_TEST_URI + NEO4J_TEST_PASSWORD,
// silent skip otherwise, run with -p 1).

func testStore(t *testing.T) *Store {
	t.Helper()
	uri := os.Getenv("NEO4J_TEST_URI")
	if uri == "" {
		t.Skip("NEO4J_TEST_URI not set")
	}
	c, err := neo4j.New(uri, envOr("NEO4J_TEST_USER", "neo4j"), os.Getenv("NEO4J_TEST_PASSWORD"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		wipeTestKeys(t, c)
		c.Close()
	})
	return NewStore(c)
}

// wipeTestKeys removes only keys owned by the test domain, never real ones.
func wipeTestKeys(t *testing.T, c *neo4j.Client) {
	t.Helper()
	ctx := context.Background()
	sess := c.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeWrite})
	defer sess.Close(ctx)
	_, err := sess.Run(ctx, `MATCH (k:ApiKey) WHERE k.owner ENDS WITH '@keys-test.invalid' DETACH DELETE k`, nil)
	if err != nil {
		t.Logf("cleanup: %v", err)
	}
}

func TestIntegration_MintValidateRevoke(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	plain, k, err := s.Mint(ctx, "Alice@keys-test.invalid", "Alice A")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if k.Owner != "alice@keys-test.invalid" {
		t.Errorf("owner not lowercased: %q", k.Owner)
	}
	if !k.ExpiresAt.After(time.Now()) {
		t.Errorf("fresh key already expired: %v", k.ExpiresAt)
	}

	got, ok, err := s.Validate(ctx, plain)
	if err != nil || !ok {
		t.Fatalf("validate fresh key: ok=%v err=%v", ok, err)
	}
	if got.Owner != "alice@keys-test.invalid" {
		t.Errorf("validate returned owner %q", got.Owner)
	}

	// Wrong key must not validate.
	if _, ok, _ := s.Validate(ctx, Prefix+"0000000000000000000000000000000000000000000000000000000000000000"); ok {
		t.Error("garbage key validated")
	}

	// last_used_at stamped by validation.
	active, ok, err := s.ActiveKey(ctx, "alice@keys-test.invalid")
	if err != nil || !ok {
		t.Fatalf("active key: ok=%v err=%v", ok, err)
	}
	if active.LastUsedAt == nil {
		t.Error("last_used_at not stamped after Validate")
	}

	// Self-revoke kills it.
	revoked, err := s.RevokeOwn(ctx, "alice@keys-test.invalid")
	if err != nil || !revoked {
		t.Fatalf("revoke: revoked=%v err=%v", revoked, err)
	}
	if _, ok, _ := s.Validate(ctx, plain); ok {
		t.Error("revoked key still validates")
	}
}

func TestIntegration_MintRevokesPreviousKey(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	first, _, err := s.Mint(ctx, "bob@keys-test.invalid", "Bob")
	if err != nil {
		t.Fatalf("mint 1: %v", err)
	}
	second, _, err := s.Mint(ctx, "bob@keys-test.invalid", "Bob")
	if err != nil {
		t.Fatalf("mint 2: %v", err)
	}

	if _, ok, _ := s.Validate(ctx, first); ok {
		t.Error("old key still valid after re-mint - one active key per owner violated")
	}
	if _, ok, _ := s.Validate(ctx, second); !ok {
		t.Error("new key not valid after re-mint")
	}

	keys, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	bobs := 0
	for _, k := range keys {
		if k.Owner == "bob@keys-test.invalid" {
			bobs++
		}
	}
	if bobs != 2 {
		t.Errorf("list shows %d keys for bob, want 2 (one revoked, one live)", bobs)
	}
}

func TestIntegration_ExpiredKeyRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Mint with a clock frozen in the past; the key expires at that month's
	// end, which is long gone, so validation with the real clock must fail.
	s.now = func() time.Time { return time.Date(2020, 1, 15, 0, 0, 0, 0, time.UTC) }
	plain, _, err := s.Mint(ctx, "carol@keys-test.invalid", "Carol")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	s.now = time.Now

	if _, ok, _ := s.Validate(ctx, plain); ok {
		t.Error("expired key validated")
	}
	if _, ok, _ := s.ActiveKey(ctx, "carol@keys-test.invalid"); ok {
		t.Error("expired key reported active")
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
