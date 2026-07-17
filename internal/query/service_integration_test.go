package query

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"graph-platform/internal/graphify"
	"graph-platform/internal/neo4j"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// testService connects to a real Neo4j using NEO4J_TEST_URI / NEO4J_TEST_USER
// (default neo4j) / NEO4J_TEST_PASSWORD, skipping the test when no URI is
// configured - same gating pattern as internal/neo4j's integration suite.
// Spin one up with e.g.:
//
//	docker run --rm -p 7688:7687 -e NEO4J_AUTH=neo4j/testpassword123 neo4j:5
//	NEO4J_TEST_URI=neo4j://127.0.0.1:7688 NEO4J_TEST_PASSWORD=testpassword123 go test ./internal/query/...
//
// This suite lives in internal/query (not internal/neo4j) because it seeds a
// graph via *neo4j.Client and then exercises Service - internal/query already
// imports internal/neo4j, so putting it the other way around would be a cycle.
func testService(t *testing.T) (*Service, *neo4j.Client) {
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

	c, err := neo4j.New(uri, user, pass)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.EnsureConstraints(context.Background()); err != nil {
		t.Fatalf("ensure constraints: %v", err)
	}
	return NewService(c), c
}

func uniqueQueryRepo(t *testing.T, tag string) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return fmt.Sprintf("qit_%s_%s", tag, hex.EncodeToString(b[:]))
}

func queryAstNode(id, label, sourceFile string) graphify.Node {
	return graphify.Node{ID: id, Label: label, NormLabel: label, SourceFile: sourceFile}
}

// wipeQueryRepo removes everything this suite could have written for repo -
// same shape as internal/neo4j's wipeRepo, duplicated here since that one is
// unexported in a different package.
func wipeQueryRepo(t *testing.T, c *neo4j.Client, repo string) {
	t.Helper()
	ctx := context.Background()
	session := c.Driver.NewSession(ctx, driver.SessionConfig{})
	defer session.Close(ctx)
	if _, err := session.Run(ctx, `MATCH (n:Entity {repo:$repo}) DETACH DELETE n`, map[string]any{"repo": repo}); err != nil {
		t.Logf("wipeQueryRepo: delete entities: %v", err)
	}
	if _, err := session.Run(ctx, `MATCH ()-[r {repo:$repo}]->() DELETE r`, map[string]any{"repo": repo}); err != nil {
		t.Logf("wipeQueryRepo: delete relationships: %v", err)
	}
	if _, err := session.Run(ctx, `MATCH (r:Repository {name:$repo}) DETACH DELETE r`, map[string]any{"repo": repo}); err != nil {
		t.Logf("wipeQueryRepo: delete repository: %v", err)
	}
}

// TestIntegration_ShortestPath_DoesNotRouteThroughRepositoryHub is bug 1's
// repro: two entities in the same repo, no real edge between them. Before the
// HAS_ENTITY rename, shortestPath's unrestricted traversal could hop
// repo->a then repo->b and report a nonsense 2-hop "path" that isn't really
// there.
func TestIntegration_ShortestPath_DoesNotRouteThroughRepositoryHub(t *testing.T) {
	svc, c := testService(t)
	ctx := context.Background()
	repo := uniqueQueryRepo(t, "hub")
	t.Cleanup(func() { wipeQueryRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	nodes := []graphify.Node{
		queryAstNode("a1", "isolatedone", "a.go"),
		queryAstNode("a2", "isolatedtwo", "b.go"),
	}
	if _, _, _, err := c.ImportNodes(ctx, repo, "c1", "r1", nodes, false); err != nil {
		t.Fatalf("import nodes: %v", err)
	}
	// Deliberately no links between a1 and a2 - only the HAS_ENTITY edges
	// from the repo hub connect them at all.

	path, err := svc.ShortestPath(ctx, "isolatedone", "isolatedtwo", nil)
	if err != nil {
		t.Fatalf("ShortestPath: %v", err)
	}
	if len(path) != 0 {
		t.Errorf("expected no path (both only connect via the Repository hub), got %d nodes: %+v", len(path), path)
	}
}

// TestIntegration_ShortestPath_FindsRealPath is the regression-safety
// counterpart: a real edge must still be found once the hub is excluded.
func TestIntegration_ShortestPath_FindsRealPath(t *testing.T) {
	svc, c := testService(t)
	ctx := context.Background()
	repo := uniqueQueryRepo(t, "real")
	t.Cleanup(func() { wipeQueryRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	nodes := []graphify.Node{
		queryAstNode("a1", "callerfn", "a.go"),
		queryAstNode("a2", "calleefn", "b.go"),
	}
	idToKey, sharedKeys, _, err := c.ImportNodes(ctx, repo, "c1", "r1", nodes, false)
	if err != nil {
		t.Fatalf("import nodes: %v", err)
	}
	links := []graphify.Link{{Source: "a1", Target: "a2", Relation: "calls"}}
	if _, _, _, err := c.ImportLinks(ctx, repo, "c1", "r1", links, idToKey, sharedKeys, false); err != nil {
		t.Fatalf("import links: %v", err)
	}

	path, err := svc.ShortestPath(ctx, "callerfn", "calleefn", nil)
	if err != nil {
		t.Fatalf("ShortestPath: %v", err)
	}
	if len(path) != 2 {
		t.Fatalf("expected a 2-node path, got %d nodes: %+v", len(path), path)
	}
	if path[1].Relationship != "CALLS" {
		t.Errorf("relationship = %q, want CALLS", path[1].Relationship)
	}
}

// TestIntegration_ShortestPath_FindsConnectedPairAmongAmbiguousNames is bug
// 2's repro: the same name ("widget") matches in two repos, and only one of
// them is actually connected to the target name ("helper"). The old
// `WITH src, dst LIMIT 1` could commit to the disconnected pair and report no
// path even though a connected pair exists.
func TestIntegration_ShortestPath_FindsConnectedPairAmongAmbiguousNames(t *testing.T) {
	svc, c := testService(t)
	ctx := context.Background()
	repoX := uniqueQueryRepo(t, "ambx")
	repoY := uniqueQueryRepo(t, "amby")
	t.Cleanup(func() { wipeQueryRepo(t, c, repoX); wipeQueryRepo(t, c, repoY) })

	if err := c.MergeRepository(ctx, repoX); err != nil {
		t.Fatalf("merge repoX: %v", err)
	}
	if err := c.MergeRepository(ctx, repoY); err != nil {
		t.Fatalf("merge repoY: %v", err)
	}

	// repoX has a same-named "widget" but nothing named "helper" at all -
	// this candidate pair can never connect to a "helper" target.
	nodesX := []graphify.Node{queryAstNode("widgetX", "widget", "x.go"), queryAstNode("otherX", "other", "x.go")}
	if _, _, _, err := c.ImportNodes(ctx, repoX, "c1", "r1", nodesX, false); err != nil {
		t.Fatalf("import nodes X: %v", err)
	}

	// repoY's "widget" really does call a "helper".
	nodesY := []graphify.Node{queryAstNode("widgetY", "widget", "y.go"), queryAstNode("helperY", "helper", "y.go")}
	idToKeyY, sharedKeysY, _, err := c.ImportNodes(ctx, repoY, "c1", "r1", nodesY, false)
	if err != nil {
		t.Fatalf("import nodes Y: %v", err)
	}
	linksY := []graphify.Link{{Source: "widgetY", Target: "helperY", Relation: "calls"}}
	if _, _, _, err := c.ImportLinks(ctx, repoY, "c1", "r1", linksY, idToKeyY, sharedKeysY, false); err != nil {
		t.Fatalf("import links Y: %v", err)
	}

	path, err := svc.ShortestPath(ctx, "widget", "helper", nil)
	if err != nil {
		t.Fatalf("ShortestPath: %v", err)
	}
	if len(path) != 2 {
		t.Fatalf("expected the connected repoY pair (2 nodes), got %d nodes: %+v", len(path), path)
	}
	if path[0].Repo != repoY || path[1].Repo != repoY {
		t.Errorf("expected both nodes from repoY, got %+v", path)
	}
}

// TestIntegration_Search_FulltextRanksExactNameFirst seeds one node whose
// name is exactly the query term (matching in both the short name and
// norm_name fields) alongside one that only matches through a long,
// multi-segment path - Lucene's field-length normalization should rank the
// exact short-field match above the diluted long-field one.
func TestIntegration_Search_FulltextRanksExactNameFirst(t *testing.T) {
	svc, c := testService(t)
	ctx := context.Background()
	repo := uniqueQueryRepo(t, "ftrank")
	t.Cleanup(func() { wipeQueryRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	nodes := []graphify.Node{
		queryAstNode("exact", "ProcessPayment", "a/short.go"),
		queryAstNode("diluted", "Other", "src/deep/legacy/module/ProcessPayment/handler/file.go"),
	}
	if _, _, _, err := c.ImportNodes(ctx, repo, "c1", "r1", nodes, false); err != nil {
		t.Fatalf("import nodes: %v", err)
	}

	results, err := svc.Search(ctx, "ProcessPayment", []string{repo})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result, got none")
	}
	if results[0].Name != "ProcessPayment" {
		t.Errorf("top result = %q, want the exact-name match ranked first; got %+v", results[0].Name, results)
	}
}

// TestIntegration_Search_FallsBackForMidIdentifierSubstring covers the case
// the fulltext tier structurally cannot handle: Lucene's tokenizer treats
// "ProcessPayment" as one whole token, so a bare substring query like
// "ProcessPay" matches no token at all. Search must still find it via the
// CONTAINS fallback.
func TestIntegration_Search_FallsBackForMidIdentifierSubstring(t *testing.T) {
	svc, c := testService(t)
	ctx := context.Background()
	repo := uniqueQueryRepo(t, "ftfallback")
	t.Cleanup(func() { wipeQueryRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	nodes := []graphify.Node{queryAstNode("n1", "ProcessPayment", "a.go")}
	if _, _, _, err := c.ImportNodes(ctx, repo, "c1", "r1", nodes, false); err != nil {
		t.Fatalf("import nodes: %v", err)
	}

	results, err := svc.Search(ctx, "ProcessPay", []string{repo})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Name == "ProcessPayment" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected ProcessPayment via the CONTAINS fallback for the mid-identifier substring %q, got %+v", "ProcessPay", results)
	}
}

// TestIntegration_Search_LuceneReservedInputReturnsCleanly makes sure a
// query built entirely from Lucene syntax characters can't reach the
// fulltext index unescaped and blow up as a parse error or an unintended
// wildcard scan; it must return normally, error or not.
func TestIntegration_Search_LuceneReservedInputReturnsCleanly(t *testing.T) {
	svc, c := testService(t)
	ctx := context.Background()
	repo := uniqueQueryRepo(t, "ftreserved")
	t.Cleanup(func() { wipeQueryRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	nodes := []graphify.Node{queryAstNode("n1", "Unrelated", "a.go")}
	if _, _, _, err := c.ImportNodes(ctx, repo, "c1", "r1", nodes, false); err != nil {
		t.Fatalf("import nodes: %v", err)
	}

	if _, err := svc.Search(ctx, `foo*bar(baz) AND "unterminated`, []string{repo}); err != nil {
		t.Errorf("Search with Lucene-reserved input returned an error, want it to return cleanly: %v", err)
	}
}

// TestIntegration_ShortestPath_SameSourceAndTarget is the repro for the
// web-UI 500: asking for a path from a symbol to itself made Neo4j's
// shortestPath throw ("start and end nodes are the same"). It must instead
// return the node as a zero-length path - and two different spellings
// resolving to one node must return empty, not error.
func TestIntegration_ShortestPath_SameSourceAndTarget(t *testing.T) {
	svc, c := testService(t)
	ctx := context.Background()
	repo := uniqueQueryRepo(t, "selfpath")
	t.Cleanup(func() { wipeQueryRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}
	nodes := []graphify.Node{
		{ID: "s1", Label: "selfSameFn()", NormLabel: "selfsamefn"},
	}
	if _, _, _, err := c.ImportNodes(ctx, repo, "c1", "r1", nodes, false); err != nil {
		t.Fatalf("import nodes: %v", err)
	}

	path, err := svc.ShortestPath(ctx, "selfSameFn()", "selfSameFn()", nil)
	if err != nil {
		t.Fatalf("ShortestPath(same, same): %v", err)
	}
	if len(path) != 1 || path[0].Name != "selfSameFn()" {
		t.Errorf("want the node itself as a zero-length path, got %+v", path)
	}

	// Different spellings, same node: filtered by src <> dst, no error.
	path, err = svc.ShortestPath(ctx, "selfSameFn()", "selfsamefn", nil)
	if err != nil {
		t.Fatalf("ShortestPath(name, norm_name): %v", err)
	}
	if len(path) != 0 {
		t.Errorf("different spellings of one node: want empty path, got %+v", path)
	}
}
