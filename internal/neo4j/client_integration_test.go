package neo4j

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"graph-platform/internal/graphify"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// testClient connects to a real Neo4j using NEO4J_TEST_URI / NEO4J_TEST_USER
// (default neo4j) / NEO4J_TEST_PASSWORD, skipping the test when no URI is
// configured. Spin one up with e.g.:
//
//	docker run --rm -p 7688:7687 -e NEO4J_AUTH=neo4j/testpassword123 neo4j:5
//	NEO4J_TEST_URI=neo4j://127.0.0.1:7688 NEO4J_TEST_PASSWORD=testpassword123 go test ./internal/neo4j/...
func testClient(t *testing.T) *Client {
	t.Helper()
	uri := os.Getenv("NEO4J_TEST_URI")
	if uri == "" {
		t.Skip("NEO4J_TEST_URI not set; run e.g. `docker run --rm -p 7688:7687 -e NEO4J_AUTH=neo4j/testpassword123 neo4j:5` " +
			"and set NEO4J_TEST_URI=neo4j://127.0.0.1:7688 NEO4J_TEST_PASSWORD=testpassword123")
	}
	user := os.Getenv("NEO4J_TEST_USER")
	if user == "" {
		user = "neo4j"
	}
	pass := os.Getenv("NEO4J_TEST_PASSWORD")

	c, err := New(uri, user, pass)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	if err := c.EnsureConstraints(context.Background()); err != nil {
		t.Fatalf("ensure constraints: %v", err)
	}
	return c
}

// uniqueRepo returns a repo name unique to this test run, so parallel/serial
// runs of this suite never collide on the same :Repository or :Entity rows.
func uniqueRepo(t *testing.T, tag string) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return fmt.Sprintf("it_%s_%s", tag, hex.EncodeToString(b[:]))
}

// uniqueID returns a random suffix for building unique platform-node IDs
// (e.g. "pkg::testlib-<suffix>") so shared-node tests don't collide with each
// other or with a real graph in the same database.
func uniqueID(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// wipeRepo removes everything this suite could have written for repo:
// repo-owned entities, edges stamped with repo, and the Repository row
// itself. Shared nodes are left alone - tests that create them are expected
// to drive them to orphan-reap via SweepStale so no manual cleanup is needed.
func wipeRepo(t *testing.T, c *Client, repo string) {
	t.Helper()
	ctx := context.Background()
	session := c.Driver.NewSession(ctx, driver.SessionConfig{})
	defer session.Close(ctx)
	if _, err := session.Run(ctx, `MATCH (n:Entity {repo:$repo}) DETACH DELETE n`, map[string]any{"repo": repo}); err != nil {
		t.Logf("wipeRepo: delete entities: %v", err)
	}
	if _, err := session.Run(ctx, `MATCH ()-[r {repo:$repo}]->() DELETE r`, map[string]any{"repo": repo}); err != nil {
		t.Logf("wipeRepo: delete relationships: %v", err)
	}
	if _, err := session.Run(ctx, `MATCH (r:Repository {name:$repo}) DETACH DELETE r`, map[string]any{"repo": repo}); err != nil {
		t.Logf("wipeRepo: delete repository: %v", err)
	}
}

// nodeExists reports whether a node with the given node_key is present.
func nodeExists(t *testing.T, c *Client, key string) bool {
	t.Helper()
	ctx := context.Background()
	session := c.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeRead})
	defer session.Close(ctx)
	res, err := session.Run(ctx, `MATCH (n:Entity {node_key:$key}) RETURN count(n) AS c`, map[string]any{"key": key})
	if err != nil {
		t.Fatalf("nodeExists query: %v", err)
	}
	rec, err := res.Single(ctx)
	if err != nil {
		t.Fatalf("nodeExists read: %v", err)
	}
	n, _ := rec.AsMap()["c"].(int64)
	return n > 0
}

func platformNode(id, label string) graphify.Node {
	return graphify.Node{ID: id, Label: label, NormLabel: label, Origin: "platform", Type: "dependency"}
}

func astNode(id, label, sourceFile string) graphify.Node {
	return graphify.Node{ID: id, Label: label, NormLabel: label, SourceFile: sourceFile}
}

func TestIntegration_ImportNodesAndLinks_CountMatches(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	repo := uniqueRepo(t, "count")
	t.Cleanup(func() { wipeRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	nodes := []graphify.Node{
		astNode("n1", "main()", "main.go"),
		astNode("n2", "helper()", "helper.go"),
		astNode("n3", "Widget", "widget.go"),
	}
	links := []graphify.Link{
		{Source: "n1", Target: "n2", Relation: "calls"},
		{Source: "n1", Target: "n3", Relation: "references"},
	}

	idToKey, _, err := c.ImportNodes(ctx, repo, "commit1", "run1", nodes, false)
	if err != nil {
		t.Fatalf("import nodes: %v", err)
	}
	if _, _, _, err := c.ImportLinks(ctx, repo, "commit1", "run1", links, idToKey, false); err != nil {
		t.Fatalf("import links: %v", err)
	}

	count, err := c.CountEntitiesForRepo(ctx, repo)
	if err != nil {
		t.Fatalf("count entities: %v", err)
	}
	if count != len(nodes) {
		t.Errorf("CountEntitiesForRepo = %d, want %d", count, len(nodes))
	}
}

func TestIntegration_SweepStale_RemovesDroppedSubsetAndVerifiesClean(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	repo := uniqueRepo(t, "sweep")
	t.Cleanup(func() { wipeRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	all := []graphify.Node{
		astNode("n1", "a()", "a.go"),
		astNode("n2", "b()", "b.go"),
		astNode("n3", "c()", "c.go"),
	}
	allLinks := []graphify.Link{
		{Source: "n1", Target: "n2", Relation: "calls"},
		{Source: "n2", Target: "n3", Relation: "calls"},
	}
	idToKey, _, err := c.ImportNodes(ctx, repo, "commit1", "run1", all, false)
	if err != nil {
		t.Fatalf("import nodes (run1): %v", err)
	}
	if _, _, _, err := c.ImportLinks(ctx, repo, "commit1", "run1", allLinks, idToKey, false); err != nil {
		t.Fatalf("import links (run1): %v", err)
	}

	// Re-import a subset: n2 (and its edges) is gone, under a new runID.
	subset := []graphify.Node{
		astNode("n1", "a()", "a.go"),
		astNode("n3", "c()", "c.go"),
	}
	idToKey2, _, err := c.ImportNodes(ctx, repo, "commit2", "run2", subset, false)
	if err != nil {
		t.Fatalf("import nodes (run2): %v", err)
	}
	if _, _, _, err := c.ImportLinks(ctx, repo, "commit2", "run2", nil, idToKey2, false); err != nil {
		t.Fatalf("import links (run2): %v", err)
	}

	nodesDeleted, relsDeleted, err := c.SweepStale(ctx, repo, "commit2", "run2")
	if err != nil {
		t.Fatalf("sweep stale: %v", err)
	}
	if nodesDeleted != 1 {
		t.Errorf("nodesDeleted = %d, want 1 (n2)", nodesDeleted)
	}
	// n2's CONTAINS edge from :Repository is stamped with repo/last_run too
	// (importNodeBatch stamps every CONTAINS edge, not just CALLS edges), so
	// three relationships go stale: CONTAINS(repo->n2), CALLS(n1->n2), CALLS(n2->n3).
	if relsDeleted != 3 {
		t.Errorf("relsDeleted = %d, want 3 (CONTAINS + both CALLS edges touching n2)", relsDeleted)
	}

	if nodeExists(t, c, idToKey["n2"]) {
		t.Error("n2 should have been swept, still exists")
	}
	if !nodeExists(t, c, idToKey["n1"]) || !nodeExists(t, c, idToKey["n3"]) {
		t.Error("survivors n1/n3 should still exist")
	}

	staleNodes, staleRels, err := c.VerifySweepClean(ctx, repo, "run2")
	if err != nil {
		t.Fatalf("verify sweep clean: %v", err)
	}
	if staleNodes != 0 || staleRels != 0 {
		t.Errorf("VerifySweepClean after a healthy sweep = (%d, %d), want (0, 0)", staleNodes, staleRels)
	}
}

func TestIntegration_SweepStale_SharedNodeAcrossRepos(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	repoA := uniqueRepo(t, "sharedA")
	repoB := uniqueRepo(t, "sharedB")
	t.Cleanup(func() { wipeRepo(t, c, repoA); wipeRepo(t, c, repoB) })

	sharedID := "pkg::testlib-" + uniqueID(t)
	shared := platformNode(sharedID, "testlib")

	if err := c.MergeRepository(ctx, repoA); err != nil {
		t.Fatalf("merge repoA: %v", err)
	}
	if err := c.MergeRepository(ctx, repoB); err != nil {
		t.Fatalf("merge repoB: %v", err)
	}

	// repoA: one own node depending on the shared package.
	ownA := astNode("ownA", "svcA", "svcA.go")
	idToKeyA, _, err := c.ImportNodes(ctx, repoA, "c1", "runA1", []graphify.Node{ownA, shared}, false)
	if err != nil {
		t.Fatalf("import nodes A: %v", err)
	}
	linksA := []graphify.Link{{Source: "ownA", Target: sharedID, Relation: "depends_on"}}
	if _, _, _, err := c.ImportLinks(ctx, repoA, "c1", "runA1", linksA, idToKeyA, false); err != nil {
		t.Fatalf("import links A: %v", err)
	}

	// repoB: same shared package, different own node.
	ownB := astNode("ownB", "svcB", "svcB.go")
	idToKeyB, _, err := c.ImportNodes(ctx, repoB, "c1", "runB1", []graphify.Node{ownB, shared}, false)
	if err != nil {
		t.Fatalf("import nodes B: %v", err)
	}
	linksB := []graphify.Link{{Source: "ownB", Target: sharedID, Relation: "depends_on"}}
	if _, _, _, err := c.ImportLinks(ctx, repoB, "c1", "runB1", linksB, idToKeyB, false); err != nil {
		t.Fatalf("import links B: %v", err)
	}

	sharedKey := idToKeyA[sharedID]
	if !nodeExists(t, c, sharedKey) {
		t.Fatal("shared node should exist after both imports")
	}

	// Sweep repoA down to empty (re-import with no nodes, new runID).
	if _, _, err := c.ImportNodes(ctx, repoA, "c2", "runA2", nil, false); err != nil {
		t.Fatalf("import nodes A (empty): %v", err)
	}
	if _, _, err := c.SweepStale(ctx, repoA, "c2", "runA2"); err != nil {
		t.Fatalf("sweep repoA: %v", err)
	}

	if !nodeExists(t, c, sharedKey) {
		t.Error("shared node should survive repoA's sweep - repoB still links it")
	}

	// Sweep repoB down to empty too - now nothing references the shared node.
	if _, _, err := c.ImportNodes(ctx, repoB, "c2", "runB2", nil, false); err != nil {
		t.Fatalf("import nodes B (empty): %v", err)
	}
	if _, _, err := c.SweepStale(ctx, repoB, "c2", "runB2"); err != nil {
		t.Fatalf("sweep repoB: %v", err)
	}

	if nodeExists(t, c, sharedKey) {
		t.Error("shared node should be reaped as an orphan once repoB's sweep removes the last edge")
	}
}

func TestIntegration_SweepStale_RefusesEmptyCommitOrRun(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	repo := uniqueRepo(t, "refuse")
	t.Cleanup(func() { wipeRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}
	nodes := []graphify.Node{astNode("n1", "a()", "a.go")}
	if _, _, err := c.ImportNodes(ctx, repo, "commit1", "run1", nodes, false); err != nil {
		t.Fatalf("import nodes: %v", err)
	}

	if _, _, err := c.SweepStale(ctx, repo, "", "run1"); err == nil {
		t.Error("SweepStale with empty commit should error")
	}
	if _, _, err := c.SweepStale(ctx, repo, "commit1", ""); err == nil {
		t.Error("SweepStale with empty runID should error")
	}

	count, err := c.CountEntitiesForRepo(ctx, repo)
	if err != nil {
		t.Fatalf("count entities: %v", err)
	}
	if count != 1 {
		t.Errorf("refused sweep deleted something: count = %d, want 1", count)
	}
}

func TestIntegration_SweepStale_EvictsLegacyUnstampedNodes(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	repo := uniqueRepo(t, "legacy")
	t.Cleanup(func() { wipeRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	// Legacy unstamped import: empty commit skips last_run/last_commit entirely.
	legacy := []graphify.Node{astNode("old1", "old()", "old.go")}
	idToKeyLegacy, _, err := c.ImportNodes(ctx, repo, "", "", legacy, false)
	if err != nil {
		t.Fatalf("legacy import: %v", err)
	}

	// A real stamped run follows, with a disjoint node set.
	fresh := []graphify.Node{astNode("new1", "new()", "new.go")}
	idToKeyFresh, _, err := c.ImportNodes(ctx, repo, "commit1", "run1", fresh, false)
	if err != nil {
		t.Fatalf("stamped import: %v", err)
	}

	if _, _, err := c.SweepStale(ctx, repo, "commit1", "run1"); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if nodeExists(t, c, idToKeyLegacy["old1"]) {
		t.Error("legacy unstamped node should have been evicted by the sweep")
	}
	if !nodeExists(t, c, idToKeyFresh["new1"]) {
		t.Error("freshly stamped node should survive")
	}
}

func TestIntegration_EnsureConstraints_Idempotent(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	if err := c.EnsureConstraints(ctx); err != nil {
		t.Fatalf("first EnsureConstraints: %v", err)
	}
	if err := c.EnsureConstraints(ctx); err != nil {
		t.Fatalf("second EnsureConstraints: %v", err)
	}
}
