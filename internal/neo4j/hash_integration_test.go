package neo4j

import (
	"context"
	"testing"

	"graph-platform/internal/graphify"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// nodeProp reads a single property off the node with the given node_key.
func nodeProp(t *testing.T, c *Client, key, prop string) any {
	t.Helper()
	ctx := context.Background()
	session := c.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeRead})
	defer session.Close(ctx)
	res, err := session.Run(ctx, `MATCH (n:Entity {node_key:$key}) RETURN n[$prop] AS v`, map[string]any{"key": key, "prop": prop})
	if err != nil {
		t.Fatalf("read %s: %v", prop, err)
	}
	rec, err := res.Single(ctx)
	if err != nil {
		t.Fatalf("read %s (single): %v", prop, err)
	}
	return rec.AsMap()["v"]
}

// setNodeProp mutates a property directly, bypassing the importer - simulates
// manual property drift a re-import should (or per the hash skip, should not)
// repair.
func setNodeProp(t *testing.T, c *Client, key, prop string, val any) {
	t.Helper()
	ctx := context.Background()
	session := c.Driver.NewSession(ctx, driver.SessionConfig{})
	defer session.Close(ctx)
	query := `MATCH (n:Entity {node_key:$key}) SET n[$prop] = $val`
	if _, err := session.Run(ctx, query, map[string]any{"key": key, "prop": prop, "val": val}); err != nil {
		t.Fatalf("set %s: %v", prop, err)
	}
}

func TestIntegration_ReimportSameContent_HashAndPropertiesUnchanged(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	repo := uniqueRepo(t, "hashsame")
	t.Cleanup(func() { wipeRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	node := astNode("n1", "a()", "a.go")
	idToKey1, _, _, err := c.ImportNodes(ctx, repo, "commit1", "run1", []graphify.Node{node}, false)
	if err != nil {
		t.Fatalf("import (run1): %v", err)
	}
	key := idToKey1["n1"]
	hash1 := nodeProp(t, c, key, "content_hash")
	if hash1 == nil || hash1 == "" {
		t.Fatal("content_hash not set after first import")
	}

	// Re-import identical content, new runID.
	idToKey2, _, _, err := c.ImportNodes(ctx, repo, "commit2", "run2", []graphify.Node{node}, false)
	if err != nil {
		t.Fatalf("import (run2): %v", err)
	}
	if idToKey2["n1"] != key {
		t.Fatalf("node_key changed across identical re-import: %s vs %s", key, idToKey2["n1"])
	}

	hash2 := nodeProp(t, c, key, "content_hash")
	if hash2 != hash1 {
		t.Errorf("content_hash changed on unchanged content: %v -> %v", hash1, hash2)
	}
	if got := nodeProp(t, c, key, "name"); got != "a()" {
		t.Errorf("name property drifted: %v", got)
	}

	nodesDeleted, relsDeleted, err := c.SweepStale(ctx, repo, "commit2", "run2")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if nodesDeleted != 0 || relsDeleted != 0 {
		t.Errorf("sweep after identical re-import deleted something: nodes=%d rels=%d", nodesDeleted, relsDeleted)
	}
}

func TestIntegration_ManualDrift_SurvivesHashSkip_RepairedByRewriteAll(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	repo := uniqueRepo(t, "drift")
	t.Cleanup(func() { wipeRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	node := astNode("n1", "a()", "a.go")
	idToKey, _, _, err := c.ImportNodes(ctx, repo, "commit1", "run1", []graphify.Node{node}, false)
	if err != nil {
		t.Fatalf("import (run1): %v", err)
	}
	key := idToKey["n1"]

	// Mutate a property directly, without touching content_hash - simulates
	// drift the importer never caused.
	setNodeProp(t, c, key, "name", "TAMPERED")

	// Re-import normally (rewriteAll=false): hash still matches the original
	// node content, so the full SET is skipped and the tampered value survives.
	if _, _, _, err := c.ImportNodes(ctx, repo, "commit2", "run2", []graphify.Node{node}, false); err != nil {
		t.Fatalf("import (run2, no rewrite): %v", err)
	}
	if got := nodeProp(t, c, key, "name"); got != "TAMPERED" {
		t.Errorf("hash-matched re-import should skip the rewrite and leave drift in place, got name=%v", got)
	}

	// Re-import with rewriteAll=true: bypasses the hash skip, repairs the value.
	if _, _, _, err := c.ImportNodes(ctx, repo, "commit3", "run3", []graphify.Node{node}, true); err != nil {
		t.Fatalf("import (run3, rewriteAll): %v", err)
	}
	if got := nodeProp(t, c, key, "name"); got != "a()" {
		t.Errorf("rewriteAll import should repair the drifted value, got name=%v", got)
	}
}

func TestIntegration_ContentChange_UpdatesPropertyAndHash(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	repo := uniqueRepo(t, "change")
	t.Cleanup(func() { wipeRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	node := astNode("n1", "a()", "a.go")
	idToKey, _, _, err := c.ImportNodes(ctx, repo, "commit1", "run1", []graphify.Node{node}, false)
	if err != nil {
		t.Fatalf("import (run1): %v", err)
	}
	key := idToKey["n1"]
	hashBefore := nodeProp(t, c, key, "content_hash")
	lineBefore := nodeProp(t, c, key, "line")

	// StableKey hashes repo+source_file+label+ID, so SourceFile can't change
	// without minting a new node_key. SourceLocation (line) isn't part of the
	// key but is part of the SET map, so bumping it changes real content on
	// the SAME node - the case this test needs.
	changed := node
	changed.SourceLocation = "42"
	if _, _, _, err := c.ImportNodes(ctx, repo, "commit2", "run2", []graphify.Node{changed}, false); err != nil {
		t.Fatalf("import (run2, changed content): %v", err)
	}

	lineAfter := nodeProp(t, c, key, "line")
	hashAfter := nodeProp(t, c, key, "content_hash")
	if lineAfter == lineBefore {
		t.Errorf("line should have changed: before=%v after=%v", lineBefore, lineAfter)
	}
	if lineAfter != "42" {
		t.Errorf("line = %v, want 42", lineAfter)
	}
	if hashAfter == hashBefore {
		t.Error("content_hash should change when real content changes")
	}
}
