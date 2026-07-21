package query

import (
	"context"
	"testing"

	"graph-platform/internal/graphify"
)

// typedNode is queryAstNode plus an explicit graphify.Node.Type, which is
// what graphify.InferLabel uses to route platform-extractor kinds (Package,
// KafkaTopic, SqlTable, GlueJob, ...) instead of the AST heuristics
// queryAstNode relies on.
func typedNode(id, label, sourceFile, typ string) graphify.Node {
	n := queryAstNode(id, label, sourceFile)
	n.Type = typ
	return n
}

// TestIntegration_FindDependents_CaseInsensitive: a NuGet/Maven-style
// PascalCase package name ("Newtonsoft.Json") must be findable by its
// natural lowercase spelling - regression test for FindDependents having
// matched on the case-sensitive `name` property instead of `name_lower`
// like every other lookup in this package.
func TestIntegration_FindDependents_CaseInsensitive(t *testing.T) {
	svc, c := testService(t)
	ctx := context.Background()
	repo := uniqueQueryRepo(t, "deps")
	t.Cleanup(func() { wipeQueryRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	nodes := []graphify.Node{
		typedNode("repo::"+repo, repo, "", "package"), // per-repo hub, matched by graphify_id prefix
		typedNode("pkg1", "Newtonsoft.Json", "go.mod", "package"),
	}
	idToKey, sharedKeys, _, err := c.ImportNodes(ctx, repo, "c1", "r1", nodes, false)
	if err != nil {
		t.Fatalf("import nodes: %v", err)
	}
	links := []graphify.Link{{Source: "repo::" + repo, Target: "pkg1", Relation: "depends_on"}}
	if _, _, _, err := c.ImportLinks(ctx, repo, "c1", "r1", links, idToKey, sharedKeys, false); err != nil {
		t.Fatalf("import links: %v", err)
	}

	got, err := svc.FindDependents(ctx, "newtonsoft.json")
	if err != nil {
		t.Fatalf("FindDependents: %v", err)
	}
	if len(got) != 1 || got[0].Name != repo {
		t.Errorf("FindDependents(lowercase) = %+v, want one edge from %q", got, repo)
	}
}

// TestIntegration_FindKafkaTopic_CaseInsensitiveMergesDuplicates: two topic
// nodes spelled differently ("PAYOUT" and "Payout" - the extractor preserves
// whatever casing source config used, so both are realistic) must both
// resolve from a lowercase query, and their producers/consumers must merge
// into ONE answer rather than the caller silently getting only one variant's
// data. Also checks a genuinely-missing topic still comes back nil (not a
// present-but-empty result now that every RETURN column is an aggregate).
func TestIntegration_FindKafkaTopic_CaseInsensitiveMergesDuplicates(t *testing.T) {
	svc, c := testService(t)
	ctx := context.Background()
	repo := uniqueQueryRepo(t, "kafka")
	t.Cleanup(func() { wipeQueryRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	nodes := []graphify.Node{
		typedNode("repo::svcA", "svcA", "", "package"),
		typedNode("repo::svcB", "svcB", "", "package"),
		typedNode("topicA", "PAYOUT", "config-a.yaml", "kafka_topic"),
		typedNode("topicB", "Payout", "config-b.yaml", "kafka_topic"),
	}
	idToKey, sharedKeys, _, err := c.ImportNodes(ctx, repo, "c1", "r1", nodes, false)
	if err != nil {
		t.Fatalf("import nodes: %v", err)
	}
	links := []graphify.Link{
		{Source: "repo::svcA", Target: "topicA", Relation: "produces"},
		{Source: "repo::svcB", Target: "topicB", Relation: "consumes"},
	}
	if _, _, _, err := c.ImportLinks(ctx, repo, "c1", "r1", links, idToKey, sharedKeys, false); err != nil {
		t.Fatalf("import links: %v", err)
	}

	got, err := svc.FindKafkaTopic(ctx, "payout")
	if err != nil {
		t.Fatalf("FindKafkaTopic: %v", err)
	}
	if got == nil {
		t.Fatal("FindKafkaTopic(lowercase) = nil, want a merged result across both case-variant nodes")
	}
	if len(got.Producers) != 1 || got.Producers[0] != "svcA" {
		t.Errorf("Producers = %v, want [svcA] (from the PAYOUT-cased node)", got.Producers)
	}
	if len(got.Consumers) != 1 || got.Consumers[0] != "svcB" {
		t.Errorf("Consumers = %v, want [svcB] (from the Payout-cased node)", got.Consumers)
	}

	if missing, err := svc.FindKafkaTopic(ctx, "does-not-exist-topic-xyz"); err != nil || missing != nil {
		t.Errorf("FindKafkaTopic(missing) = %+v, %v; want nil, nil", missing, err)
	}
}

// TestIntegration_FindSQLObject_CaseInsensitive: SQL Server's default
// collation is case-insensitive, so "dbo.Orders" as stored must still be
// found by "dbo"/"orders" typed in any case, both via the fully-qualified
// match and the schema-less bare-name fallback.
func TestIntegration_FindSQLObject_CaseInsensitive(t *testing.T) {
	svc, c := testService(t)
	ctx := context.Background()
	repo := uniqueQueryRepo(t, "sql")
	t.Cleanup(func() { wipeQueryRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	nodes := []graphify.Node{
		typedNode("sqlT1", "dbo.Orders", "schema.sql", "sql_table"),
	}
	if _, _, _, err := c.ImportNodes(ctx, repo, "c1", "r1", nodes, false); err != nil {
		t.Fatalf("import nodes: %v", err)
	}

	got, err := svc.FindSQLObject(ctx, "DBO", "orders")
	if err != nil {
		t.Fatalf("FindSQLObject(qualified, mixed case): %v", err)
	}
	if len(got) != 1 || got[0].Name != "Orders" {
		t.Errorf("FindSQLObject(qualified) = %+v, want one object named Orders", got)
	}

	got, err = svc.FindSQLObject(ctx, "", "ORDERS")
	if err != nil {
		t.Fatalf("FindSQLObject(bare, uppercase): %v", err)
	}
	if len(got) != 1 || got[0].Name != "Orders" {
		t.Errorf("FindSQLObject(bare fallback) = %+v, want one object named Orders", got)
	}
}

// TestIntegration_FindGlueJobs_CaseInsensitiveSourceTarget: source/target
// table names are frequently the same SQL Server tables FindSQLObject
// describes, so they need the same case-insensitive treatment.
func TestIntegration_FindGlueJobs_CaseInsensitiveSourceTarget(t *testing.T) {
	svc, c := testService(t)
	ctx := context.Background()
	repo := uniqueQueryRepo(t, "glue")
	t.Cleanup(func() { wipeQueryRepo(t, c, repo) })

	if err := c.MergeRepository(ctx, repo); err != nil {
		t.Fatalf("merge repository: %v", err)
	}

	nodes := []graphify.Node{
		typedNode("job1", "etl-job-1", "job1.py", "glue_job"),
		typedNode("src1", "raw.Events", "", "sql_table"),
		typedNode("tgt1", "curated.Events", "", "sql_table"),
	}
	idToKey, sharedKeys, _, err := c.ImportNodes(ctx, repo, "c1", "r1", nodes, false)
	if err != nil {
		t.Fatalf("import nodes: %v", err)
	}
	links := []graphify.Link{
		{Source: "job1", Target: "src1", Relation: "reads_source"},
		{Source: "job1", Target: "tgt1", Relation: "writes_destination"},
	}
	if _, _, _, err := c.ImportLinks(ctx, repo, "c1", "r1", links, idToKey, sharedKeys, false); err != nil {
		t.Fatalf("import links: %v", err)
	}

	bySource, err := svc.FindGlueJobs(ctx, "raw.events", "")
	if err != nil {
		t.Fatalf("FindGlueJobs(source, lowercase): %v", err)
	}
	if len(bySource) != 1 || bySource[0].Name != "etl-job-1" {
		t.Errorf("FindGlueJobs(lowercase source) = %+v, want etl-job-1", bySource)
	}

	byTarget, err := svc.FindGlueJobs(ctx, "", "CURATED.EVENTS")
	if err != nil {
		t.Fatalf("FindGlueJobs(target, uppercase): %v", err)
	}
	if len(byTarget) != 1 || byTarget[0].Name != "etl-job-1" {
		t.Errorf("FindGlueJobs(uppercase target) = %+v, want etl-job-1", byTarget)
	}
}
