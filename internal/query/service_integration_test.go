package query

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"a1-knowledge-graph/internal/graphify"
	"a1-knowledge-graph/internal/neo4j"

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

// TestIntegration_ShortestPath_DoesNotRouteThroughRepositoryHub: two
// entities in the same repo with no real edge between them must yield no
// path - the traversal must not hop repo->a then repo->b through the
// Repository hub that owns both.
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

// TestIntegration_ShortestPath_FindsRealPath: a real edge must still be
// found with the hub excluded.
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
	links := []graphify.Link{{Source: "a1", Target: "a2", Relation: "calls", Confidence: "EXTRACTED"}}
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
	if path[0].RelConfidence != "" {
		t.Errorf("first node carries a rel confidence %q, want empty (no inbound edge)", path[0].RelConfidence)
	}
	if path[1].RelConfidence != "EXTRACTED" {
		t.Errorf("rel_confidence = %q, want EXTRACTED (stamped on the seeded edge)", path[1].RelConfidence)
	}

	// The same edge's confidence must also surface on the callers/callees
	// views - they read it from a different query than ShortestPath does.
	callers, err := svc.FindCallers(ctx, "calleefn", nil)
	if err != nil {
		t.Fatalf("FindCallers: %v", err)
	}
	if len(callers) != 1 || callers[0].Confidence != "EXTRACTED" {
		t.Errorf("FindCallers confidence = %+v, want one edge with EXTRACTED", callers)
	}
	callees, err := svc.FindCallees(ctx, "callerfn", nil)
	if err != nil {
		t.Fatalf("FindCallees: %v", err)
	}
	if len(callees) != 1 || callees[0].Confidence != "EXTRACTED" {
		t.Errorf("FindCallees confidence = %+v, want one edge with EXTRACTED", callees)
	}
}

// TestIntegration_ExactMatch_ToleratesTrailingParens: a symbol typed the way
// people write it in code ("ConvertPosition()") must match exactly like the
// bare name across every exact-match query - FindSymbol, FindCallers,
// FindCallees, BlastRadius, and ShortestPath (both directions and the
// same-symbol self-path case). Regression test for a real bug: the graph
// never stores parens in a name, so leaving them in silently zeroed out
// every one of these despite the underlying CALLS edges being real.
func TestIntegration_ExactMatch_ToleratesTrailingParens(t *testing.T) {
	svc, c := testService(t)
	ctx := context.Background()
	repo := uniqueQueryRepo(t, "parens")
	t.Cleanup(func() { wipeQueryRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	nodes := []graphify.Node{
		queryAstNode("h1", "convertPositionController", "handler.go"),
		queryAstNode("h2", "ConvertPosition", "provider.go"),
	}
	idToKey, sharedKeys, _, err := c.ImportNodes(ctx, repo, "c1", "r1", nodes, false)
	if err != nil {
		t.Fatalf("import nodes: %v", err)
	}
	links := []graphify.Link{{Source: "h1", Target: "h2", Relation: "calls"}}
	if _, _, _, err := c.ImportLinks(ctx, repo, "c1", "r1", links, idToKey, sharedKeys, false); err != nil {
		t.Fatalf("import links: %v", err)
	}

	if got, err := svc.FindSymbol(ctx, "ConvertPosition()", nil); err != nil || len(got) != 1 {
		t.Errorf("FindSymbol(with parens) = %v, %v; want 1 result", got, err)
	}
	if got, err := svc.FindCallers(ctx, "ConvertPosition()", nil); err != nil || len(got) != 1 {
		t.Errorf("FindCallers(with parens) = %v, %v; want 1 caller", got, err)
	}
	if got, err := svc.FindCallees(ctx, "convertPositionController()", nil); err != nil || len(got) != 1 {
		t.Errorf("FindCallees(with parens) = %v, %v; want 1 callee", got, err)
	}
	if got, err := svc.BlastRadius(ctx, "convertPositionController()", 0, nil); err != nil || len(got) != 1 {
		t.Errorf("BlastRadius(with parens) = %v, %v; want 1 reachable node", got, err)
	}
	path, err := svc.ShortestPath(ctx, "convertPositionController()", "ConvertPosition()", nil)
	if err != nil || len(path) != 2 {
		t.Errorf("ShortestPath(with parens) = %v, %v; want a 2-node path", path, err)
	}
	self, err := svc.ShortestPath(ctx, "ConvertPosition()", "ConvertPosition", nil)
	if err != nil || len(self) != 1 {
		t.Errorf("ShortestPath(same symbol, one with parens) = %v, %v; want the 1-node self-path", self, err)
	}
}

// TestIntegration_ExactMatch_ParenSuffixedFunctionNames seeds Function nodes
// named the way graphify ACTUALLY stores them - with a trailing "()"
// ("GetDepositService()"; InferLabel detects functions by that exact suffix,
// and the entry-point queries match 'main()') - and asserts every exact-match
// query finds them from BOTH typed spellings. Regression test for a P0: a
// previous fix stripped "()" from user input before matching on the wrong
// assumption that stored names are bare, which made Function nodes
// unfindable by any spelling in callers/callees/blast-radius/path.
func TestIntegration_ExactMatch_ParenSuffixedFunctionNames(t *testing.T) {
	svc, c := testService(t)
	ctx := context.Background()
	repo := uniqueQueryRepo(t, "fnparens")
	t.Cleanup(func() { wipeQueryRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	nodes := []graphify.Node{
		queryAstNode("d1", "depositController()", "controller.go"),
		queryAstNode("d2", "GetDepositService()", "service.go"),
	}
	idToKey, sharedKeys, _, err := c.ImportNodes(ctx, repo, "c1", "r1", nodes, false)
	if err != nil {
		t.Fatalf("import nodes: %v", err)
	}
	links := []graphify.Link{{Source: "d1", Target: "d2", Relation: "calls", Confidence: "EXTRACTED"}}
	if _, _, _, err := c.ImportLinks(ctx, repo, "c1", "r1", links, idToKey, sharedKeys, false); err != nil {
		t.Fatalf("import links: %v", err)
	}

	for _, typed := range []string{"GetDepositService", "GetDepositService()"} {
		if got, err := svc.FindSymbol(ctx, typed, nil); err != nil || len(got) != 1 {
			t.Errorf("FindSymbol(%q) = %v, %v; want 1 result", typed, got, err)
		}
		if got, err := svc.FindCallers(ctx, typed, nil); err != nil || len(got) != 1 {
			t.Errorf("FindCallers(%q) = %v, %v; want 1 caller", typed, got, err)
		}
		if got, err := svc.FindSymbol(ctx, typed, []string{repo}); err != nil || len(got) != 1 {
			t.Errorf("FindSymbol(%q, repo-scoped) = %v, %v; want 1 result", typed, got, err)
		}
	}
	for _, typed := range []string{"depositController", "depositController()"} {
		if got, err := svc.FindCallees(ctx, typed, nil); err != nil || len(got) != 1 {
			t.Errorf("FindCallees(%q) = %v, %v; want 1 callee", typed, got, err)
		}
		if got, err := svc.BlastRadius(ctx, typed, 0, nil); err != nil || len(got) != 1 {
			t.Errorf("BlastRadius(%q) = %v, %v; want 1 reachable node", typed, got, err)
		}
	}
	if path, err := svc.ShortestPath(ctx, "depositController", "GetDepositService()", nil); err != nil || len(path) != 2 {
		t.Errorf("ShortestPath(mixed spellings) = %v, %v; want a 2-node path", path, err)
	}
	if self, err := svc.ShortestPath(ctx, "GetDepositService()", "getdepositservice", nil); err != nil || len(self) != 1 {
		t.Errorf("ShortestPath(same fn, different spellings) = %v, %v; want the 1-node self-path", self, err)
	}
}

// TestIntegration_ShortestPath_FindsConnectedPairAmongAmbiguousNames: the
// same name ("widget") matches in two repos and only one of them is
// connected to the target name ("helper"); the connected pair must be
// found rather than committing to an arbitrary disconnected pair.
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

// TestIntegration_ShortestPath_SameSourceAndTarget: a path from a symbol
// to itself must return the node as a zero-length path (Neo4j's
// shortestPath rejects identical endpoints, so this must never reach it)
// - and two different spellings
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

// TestIntegration_Freshness: StampRepoSync records last_synced_at (and
// last_indexed_at when indexed) on the Repository node, and Freshness
// returns them.
func TestIntegration_Freshness(t *testing.T) {
	svc, c := testService(t)
	ctx := context.Background()
	repo := uniqueQueryRepo(t, "fresh")
	t.Cleanup(func() { wipeQueryRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}
	if err := c.StampRepoSync(ctx, repo, false); err != nil {
		t.Fatalf("stamp (synced only): %v", err)
	}

	find := func() (RepoFreshness, bool) {
		rows, err := svc.Freshness(ctx)
		if err != nil {
			t.Fatalf("Freshness: %v", err)
		}
		for _, r := range rows {
			if r.Repo == repo {
				return r, true
			}
		}
		return RepoFreshness{}, false
	}

	row, ok := find()
	if !ok {
		t.Fatal("stamped repo missing from Freshness result")
	}
	if time.Since(row.LastSyncedAt) > time.Minute || row.LastSyncedAt.IsZero() {
		t.Errorf("last_synced_at not recent: %v", row.LastSyncedAt)
	}
	if !row.LastIndexedAt.IsZero() {
		t.Errorf("last_indexed_at should be unset after a synced-only stamp, got %v", row.LastIndexedAt)
	}

	if err := c.StampRepoSync(ctx, repo, true); err != nil {
		t.Fatalf("stamp (indexed): %v", err)
	}
	row, ok = find()
	if !ok {
		t.Fatal("repo missing after indexed stamp")
	}
	if row.LastIndexedAt.IsZero() || time.Since(row.LastIndexedAt) > time.Minute {
		t.Errorf("last_indexed_at not recent after indexed stamp: %v", row.LastIndexedAt)
	}
}
