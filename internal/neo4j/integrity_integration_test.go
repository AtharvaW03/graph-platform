package neo4j

import (
	"context"
	"fmt"
	"testing"

	"graph-platform/internal/graphify"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// countRelType counts relationships of relType directly from src's node_key
// to tgt's node_key. relType is always a literal in this file, never
// user input, so interpolating it is safe (same reasoning as the production
// allowlist-then-interpolate pattern).
func countRelType(t *testing.T, c *Client, srcKey, tgtKey, relType string) int {
	t.Helper()
	ctx := context.Background()
	session := c.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeRead})
	defer session.Close(ctx)
	query := fmt.Sprintf(`MATCH (a:Entity {node_key:$s})-[r:%s]->(b:Entity {node_key:$t}) RETURN count(r) AS c`, relType)
	res, err := session.Run(ctx, query, map[string]any{"s": srcKey, "t": tgtKey})
	if err != nil {
		t.Fatalf("countRelType query: %v", err)
	}
	rec, err := res.Single(ctx)
	if err != nil {
		t.Fatalf("countRelType read: %v", err)
	}
	n, _ := rec.AsMap()["c"].(int64)
	return int(n)
}

// relRepoValues returns the repo property of every relType relationship
// between src and tgt's node_keys, so a test can check which repo(s) own the
// surviving parallel edges.
func relRepoValues(t *testing.T, c *Client, srcKey, tgtKey, relType string) []string {
	t.Helper()
	ctx := context.Background()
	session := c.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeRead})
	defer session.Close(ctx)
	query := fmt.Sprintf(`MATCH (a:Entity {node_key:$s})-[r:%s]->(b:Entity {node_key:$t}) RETURN r.repo AS repo`, relType)
	res, err := session.Run(ctx, query, map[string]any{"s": srcKey, "t": tgtKey})
	if err != nil {
		t.Fatalf("relRepoValues query: %v", err)
	}
	recs, err := res.Collect(ctx)
	if err != nil {
		t.Fatalf("relRepoValues read: %v", err)
	}
	out := make([]string, 0, len(recs))
	for _, rec := range recs {
		repo, _ := rec.AsMap()["repo"].(string)
		out = append(out, repo)
	}
	return out
}

// TestIntegration_SharedSharedEdge_OwnershipDoesNotFlap: two repos
// independently emit the same shared-to-shared edge (both endpoints
// org-global, e.g. two SQL objects). Each repo must get its own parallel
// edge instance, keyed by repo, so one repo's sweep only ever touches its
// own.
func TestIntegration_SharedSharedEdge_OwnershipDoesNotFlap(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	repoA := uniqueRepo(t, "flapA")
	repoB := uniqueRepo(t, "flapB")
	t.Cleanup(func() { wipeRepo(t, c, repoA); wipeRepo(t, c, repoB) })

	if err := c.MergeRepository(ctx, repoA); err != nil {
		t.Fatalf("merge repoA: %v", err)
	}
	if err := c.MergeRepository(ctx, repoB); err != nil {
		t.Fatalf("merge repoB: %v", err)
	}

	// Both endpoints shared (sql:: prefix, platform origin); both repos
	// assert the same table-in-schema fact independently.
	suffix := uniqueID(t)
	schemaID := "sql::sql_schema::" + suffix
	tableID := "sql::sql_table::" + suffix
	schema := platformNode(schemaID, "dbo")
	table := platformNode(tableID, "dbo.trades")

	idToKeyA, sharedKeysA, _, err := c.ImportNodes(ctx, repoA, "c1", "rA1", []graphify.Node{schema, table}, false)
	if err != nil {
		t.Fatalf("import nodes A: %v", err)
	}
	linksA := []graphify.Link{{Source: tableID, Target: schemaID, Relation: "in_schema"}}
	if _, _, _, err := c.ImportLinks(ctx, repoA, "c1", "rA1", linksA, idToKeyA, sharedKeysA, false); err != nil {
		t.Fatalf("import links A: %v", err)
	}

	idToKeyB, sharedKeysB, _, err := c.ImportNodes(ctx, repoB, "c1", "rB1", []graphify.Node{schema, table}, false)
	if err != nil {
		t.Fatalf("import nodes B: %v", err)
	}
	linksB := []graphify.Link{{Source: tableID, Target: schemaID, Relation: "in_schema"}}
	if _, _, _, err := c.ImportLinks(ctx, repoB, "c1", "rB1", linksB, idToKeyB, sharedKeysB, false); err != nil {
		t.Fatalf("import links B: %v", err)
	}

	tableKey := idToKeyA[tableID]
	schemaKey := idToKeyA[schemaID]

	if got := countRelType(t, c, tableKey, schemaKey, "IN_SCHEMA"); got != 2 {
		t.Fatalf("IN_SCHEMA edges after both imports = %d, want 2 (one parallel edge per repo)", got)
	}

	// repoA's next run no longer references this relationship (its own SQL
	// dropped it) - its sweep should remove only repoA's own edge instance.
	if _, _, _, err := c.ImportNodes(ctx, repoA, "c2", "rA2", nil, false); err != nil {
		t.Fatalf("import nodes A (empty): %v", err)
	}
	if _, _, err := c.SweepStale(ctx, repoA, "c2", "rA2"); err != nil {
		t.Fatalf("sweep repoA: %v", err)
	}

	survivors := relRepoValues(t, c, tableKey, schemaKey, "IN_SCHEMA")
	if len(survivors) != 1 {
		t.Fatalf("IN_SCHEMA edges after repoA's sweep = %d, want 1 (repoB's survivor): %v", len(survivors), survivors)
	}
	if survivors[0] != repoB {
		t.Errorf("surviving edge owner = %q, want %q (repoA's sweep must not touch repoB's edge)", survivors[0], repoB)
	}

	// Clean up properly rather than leaving this to wipeRepo: wipeRepo deletes
	// any relationship stamped with repoB, which would orphan the shared
	// schema/table nodes as a side effect without ever running the orphan-reap
	// step - leaking orphaned shared nodes into whatever test runs next and
	// inflating its nodesDeleted count. Sweeping repoB down to empty here
	// drives the proper reap within this test instead.
	if _, _, _, err := c.ImportNodes(ctx, repoB, "c2", "rB2", nil, false); err != nil {
		t.Fatalf("import nodes B (empty): %v", err)
	}
	if _, _, err := c.SweepStale(ctx, repoB, "c2", "rB2"); err != nil {
		t.Fatalf("sweep repoB: %v", err)
	}
	if nodeExists(t, c, schemaKey) || nodeExists(t, c, tableKey) {
		t.Error("shared schema/table nodes should have been reaped once repoB's sweep removed the last edge")
	}
}

// nodeLabelsFor returns every label on the node with the given node_key.
func nodeLabelsFor(t *testing.T, c *Client, key string) []string {
	t.Helper()
	ctx := context.Background()
	session := c.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeRead})
	defer session.Close(ctx)
	res, err := session.Run(ctx, `MATCH (n:Entity {node_key:$key}) RETURN labels(n) AS l`, map[string]any{"key": key})
	if err != nil {
		t.Fatalf("nodeLabelsFor query: %v", err)
	}
	rec, err := res.Single(ctx)
	if err != nil {
		t.Fatalf("nodeLabelsFor read: %v", err)
	}
	raw, _ := rec.AsMap()["l"].([]any)
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		if s, ok := l.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// TestIntegration_KindChange_ReplacesLabelNotAccumulates: re-importing the
// same node_key with a different inferred kind must replace the code label,
// not accumulate a second one. Type (not Label) drives the kind because
// StableKey hashes only repo+SourceFile+Label+ID, so changing Type keeps
// node_key identical while changing InferLabel's result.
func TestIntegration_KindChange_ReplacesLabelNotAccumulates(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	repo := uniqueRepo(t, "kind")
	t.Cleanup(func() { wipeRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	base := graphify.Node{ID: "x1", Label: "thing", NormLabel: "thing", SourceFile: "w.go"}
	first := base
	first.Type = "kafka_topic" // InferLabel -> KafkaTopic
	second := base
	second.Type = "glue_job" // InferLabel -> GlueJob, same node_key as first

	idToKey1, _, _, err := c.ImportNodes(ctx, repo, "c1", "r1", []graphify.Node{first}, false)
	if err != nil {
		t.Fatalf("import (kafka_topic): %v", err)
	}
	key := idToKey1["x1"]
	labels1 := nodeLabelsFor(t, c, key)
	if !hasLabel(labels1, "KafkaTopic") {
		t.Fatalf("first import labels = %v, want KafkaTopic present", labels1)
	}

	idToKey2, _, _, err := c.ImportNodes(ctx, repo, "c2", "r2", []graphify.Node{second}, false)
	if err != nil {
		t.Fatalf("import (glue_job): %v", err)
	}
	if idToKey2["x1"] != key {
		t.Fatalf("node_key changed across the kind change (%s vs %s) - test setup invalid, StableKey should be unaffected by Type", key, idToKey2["x1"])
	}

	labels2 := nodeLabelsFor(t, c, key)
	if !hasLabel(labels2, "GlueJob") {
		t.Errorf("after kind change, labels = %v, want GlueJob present", labels2)
	}
	if hasLabel(labels2, "KafkaTopic") {
		t.Errorf("after kind change, labels = %v, want KafkaTopic REMOVED (this is the bug: labels accumulating instead of replacing)", labels2)
	}
}
