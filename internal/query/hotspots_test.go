package query

import (
	"testing"
	"time"
)

func TestHotspotCache(t *testing.T) {
	now := time.Now()
	c := newHotspotCache(5 * time.Minute)
	c.now = func() time.Time { return now }

	if _, ok := c.get(25); ok {
		t.Fatal("empty cache should miss")
	}

	nodes := []HotspotNode{{Name: "OrderService", Repo: "orders-service", FanIn: 42}}
	c.put(25, nodes)

	got, ok := c.get(25)
	if !ok || len(got) != 1 || got[0].Name != "OrderService" {
		t.Fatalf("expected cache hit with stored nodes, got ok=%v nodes=%v", ok, got)
	}

	// Different limit is a different entry.
	if _, ok := c.get(50); ok {
		t.Fatal("limit 50 should miss when only 25 is cached")
	}

	// Within TTL: still a hit.
	now = now.Add(4 * time.Minute)
	if _, ok := c.get(25); !ok {
		t.Fatal("entry should still be valid within TTL")
	}

	// Past TTL: miss.
	now = now.Add(2 * time.Minute)
	if _, ok := c.get(25); ok {
		t.Fatal("entry should expire after TTL")
	}
}
