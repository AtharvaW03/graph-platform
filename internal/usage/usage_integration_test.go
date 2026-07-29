package usage

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	"a1-knowledge-graph/internal/neo4j"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Integration tests: NEO4J_TEST_URI + NEO4J_TEST_PASSWORD, silent skip
// otherwise, run with -p 1 (shared test database).

const testActorSuffix = "@usage-test.invalid"

func testDB(t *testing.T) *neo4j.Client {
	t.Helper()
	uri := os.Getenv("NEO4J_TEST_URI")
	if uri == "" {
		t.Skip("NEO4J_TEST_URI not set")
	}
	user := os.Getenv("NEO4J_TEST_USER")
	if user == "" {
		user = "neo4j"
	}
	c, err := neo4j.New(uri, user, os.Getenv("NEO4J_TEST_PASSWORD"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		wipe(t, c)
		c.Close()
	})
	wipe(t, c)
	return c
}

func wipe(t *testing.T, c *neo4j.Client) {
	t.Helper()
	ctx := context.Background()
	sess := c.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeWrite})
	defer sess.Close(ctx)
	for _, q := range []string{
		`MATCH (u:UsageStat) WHERE u.actor ENDS WITH '` + testActorSuffix + `' DETACH DELETE u`,
		`MATCH (a:RepoAccess) WHERE a.actor ENDS WITH '` + testActorSuffix + `' DETACH DELETE a`,
	} {
		if _, err := sess.Run(ctx, q, nil); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}
}

// flushNow records samples and forces a synchronous flush by closing.
func flushNow(t *testing.T, db *neo4j.Client, record func(*Recorder)) {
	t.Helper()
	r := NewRecorder(db, log.New(os.Stderr, "", 0))
	record(r)
	r.Close() // drains and flushes
}

func TestIntegration_RecordAndRank(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	alice := "alice" + testActorSuffix
	bob := "bob" + testActorSuffix

	flushNow(t, db, func(r *Recorder) {
		for i := 0; i < 5; i++ {
			r.Record(alice, "search", []string{"payments"})
		}
		r.Record(alice, "search", []string{"auth"})
		r.Record(bob, "routes", []string{"payments"})
	})

	rd := NewReader(db)

	users, err := rd.TopUsers(ctx, 7, 10)
	if err != nil {
		t.Fatalf("top users: %v", err)
	}
	var aliceCount, bobCount int
	for _, u := range users {
		switch u.Name {
		case alice:
			aliceCount = u.Count
		case bob:
			bobCount = u.Count
		}
	}
	if aliceCount != 6 {
		t.Errorf("alice count = %d, want 6", aliceCount)
	}
	if bobCount != 1 {
		t.Errorf("bob count = %d, want 1", bobCount)
	}
	if aliceCount < bobCount {
		t.Error("ranking is not descending by volume")
	}

	repos, err := rd.TopRepos(ctx, 7, 10)
	if err != nil {
		t.Fatalf("top repos: %v", err)
	}
	found := false
	for _, r := range repos {
		if r.Name == "payments" {
			found = true
			if r.Count != 6 { // 5 from alice + 1 from bob
				t.Errorf("payments count = %d, want 6", r.Count)
			}
		}
	}
	if !found {
		t.Error("payments missing from top repos")
	}

	// Per-actor drill-down.
	activity, err := rd.ActorActivity(ctx, alice, 7)
	if err != nil {
		t.Fatalf("actor activity: %v", err)
	}
	if len(activity) != 2 {
		t.Errorf("alice touched %d repos, want 2", len(activity))
	}
}

func TestIntegration_AggregatesRepeatedSamples(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	actor := "heavy" + testActorSuffix

	flushNow(t, db, func(r *Recorder) {
		for i := 0; i < 50; i++ {
			r.Record(actor, "search", []string{"same-repo"})
		}
	})

	// 50 identical samples must fold into ONE UsageStat row with count 50.
	sess := db.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeRead})
	defer sess.Close(ctx)
	res, err := sess.Run(ctx, `MATCH (u:UsageStat {actor: $actor}) RETURN count(u) AS rows, sum(u.count) AS total`, map[string]any{"actor": actor})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := res.Single(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := rec.AsMap()
	if got := asInt(m["rows"]); got != 1 {
		t.Errorf("wrote %d UsageStat rows, want 1 (aggregation failed)", got)
	}
	if got := asInt(m["total"]); got != 50 {
		t.Errorf("total count = %d, want 50", got)
	}
}

func TestIntegration_AnomalyDetection(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	normal := "normal" + testActorSuffix
	sweeper := "sweeper" + testActorSuffix

	flushNow(t, db, func(r *Recorder) {
		// Normal usage: a couple of repos, many requests.
		for i := 0; i < 20; i++ {
			r.Record(normal, "search", []string{"payments"})
			r.Record(normal, "callers", []string{"auth"})
		}
		// The leaver pattern: one request each across many repos.
		for i := 0; i < 15; i++ {
			r.Record(sweeper, "overview", []string{repoName(i)})
		}
	})

	rd := NewReader(db)
	anomalies, err := rd.Anomalies(ctx, time.Hour, 10)
	if err != nil {
		t.Fatalf("anomalies: %v", err)
	}

	var sawSweeper, sawNormal bool
	for _, a := range anomalies {
		switch a.Actor {
		case sweeper:
			sawSweeper = true
			if a.RepoCount != 15 {
				t.Errorf("sweeper repo count = %d, want 15", a.RepoCount)
			}
		case normal:
			sawNormal = true
		}
	}
	if !sawSweeper {
		t.Error("enumeration across 15 repos did not trigger the anomaly detector")
	}
	if sawNormal {
		t.Error("normal 2-repo usage was flagged as an anomaly (false positive)")
	}

	// A higher threshold must not flag the same activity.
	quiet, err := rd.Anomalies(ctx, time.Hour, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range quiet {
		if a.Actor == sweeper {
			t.Error("threshold 50 still flagged a 15-repo sweep")
		}
	}
}

func TestIntegration_TrafficAndTotals(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	actor := "traffic" + testActorSuffix

	flushNow(t, db, func(r *Recorder) {
		for i := 0; i < 7; i++ {
			r.Record(actor, "search", nil) // no repo scope
		}
	})

	rd := NewReader(db)
	total, err := rd.TotalRequests(ctx, 7)
	if err != nil {
		t.Fatalf("total: %v", err)
	}
	if total < 7 {
		t.Errorf("total = %d, want at least 7", total)
	}

	series, err := rd.Traffic(ctx, 7)
	if err != nil {
		t.Fatalf("traffic: %v", err)
	}
	if len(series) == 0 {
		t.Error("traffic series is empty after recording")
	}

	// Endpoint ranking sees the no-repo samples too.
	eps, err := rd.TopEndpoints(ctx, 7, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range eps {
		if e.Name == "search" {
			found = true
		}
	}
	if !found {
		t.Error("search endpoint missing from ranking")
	}
}

func repoName(i int) string {
	return "repo-" + string(rune('a'+i))
}
