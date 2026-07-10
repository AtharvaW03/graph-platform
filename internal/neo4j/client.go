package neo4j

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"graph-platform/internal/graphify"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// orNil turns "" into nil so `SET n += {...}` skips the property instead of
// storing an empty string.
func orNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const batchSize = 500

// labelAllowlist is every label the importer may set on an Entity. Labels
// can't be parameterized in Cypher, so they're interpolated into the query;
// this allowlist is the injection guard. New labels must also be returned by
// graphify.InferLabel.
var labelAllowlist = map[string]bool{
	// graphify core
	"File": true, "Function": true, "Class": true,
	"Package": true, "DocSection": true, "Symbol": true,

	// extractor-plugin entities
	"HttpRoute":     true,
	"KafkaTopic":    true,
	"KafkaProducer": true,
	"KafkaConsumer": true,
	"SqlSchema":     true,
	"SqlTable":      true,
	"SqlView":       true,
	"SqlProcedure":  true,
	"SqlTrigger":    true,
	"SqlFunction":   true,
	"GlueJob":       true,
	"GlueSchedule":  true,
}

// metadataProps are the metadata keys promoted to node properties at import;
// every other metadata key is dropped. Add a key here only if the query layer
// reads it.
var metadataProps = []string{
	"version", "scope", "manifest", // deps
	"method", "handler", "source", "classification", "documented", "tags", // httpapi
	"script", "schedule", "sources", "dests", "expression", // glue
	"is_repository", "discovered_as", // deps repo hubs
	"schema", "object_name", // mssql
}

type Client struct {
	Driver driver.DriverWithContext
}

func New(uri, username, password string) (*Client, error) {
	d, err := driver.NewDriverWithContext(uri, driver.BasicAuth(username, password, ""))
	if err != nil {
		return nil, err
	}
	if err := d.VerifyConnectivity(context.Background()); err != nil {
		_ = d.Close(context.Background())
		return nil, err
	}
	return &Client{Driver: d}, nil
}

func (c *Client) Close() error {
	return c.Driver.Close(context.Background())
}

// VerifyConnectivity probes the driver, e.g. before an indexing cycle.
func (c *Client) VerifyConnectivity(ctx context.Context) error {
	return c.Driver.VerifyConnectivity(ctx)
}

// EnsureConstraints creates the node_key/Repository.name uniqueness
// constraints, the repo index, and TEXT indexes on the pre-lowercased name
// columns. All idempotent.
//
// The name_lower / norm_name_lower indexes back case-insensitive lookups:
// toLower(n.name) can't use an index, so the importer stores a lowercased
// column and queries compare against it directly.
func (c *Client) EnsureConstraints(ctx context.Context) error {
	session := c.Driver.NewSession(ctx, driver.SessionConfig{})
	defer session.Close(ctx)

	stmts := []string{
		`CREATE CONSTRAINT entity_key IF NOT EXISTS FOR (n:Entity) REQUIRE n.node_key IS UNIQUE`,
		`CREATE INDEX entity_repo IF NOT EXISTS FOR (n:Entity) ON (n.repo)`,
		`CREATE CONSTRAINT repo_name IF NOT EXISTS FOR (r:Repository) REQUIRE r.name IS UNIQUE`,
		`CREATE TEXT INDEX entity_name_lower IF NOT EXISTS FOR (n:Entity) ON (n.name_lower)`,
		`CREATE TEXT INDEX entity_norm_name_lower IF NOT EXISTS FOR (n:Entity) ON (n.norm_name_lower)`,
		// Backs the sweep's shared-node lookups (SweepStale step 3, VerifySweepClean).
		`CREATE INDEX entity_shared IF NOT EXISTS FOR (n:Entity) ON (n.shared)`,
		// The writer lease is a singleton row; this constraint is what makes a
		// concurrent first-ever AcquireLease race safe (MERGE alone can still
		// double-create without it).
		`CREATE CONSTRAINT indexer_lease_id IF NOT EXISTS FOR (l:IndexerLease) REQUIRE l.id IS UNIQUE`,
	}
	for _, q := range stmts {
		if _, err := session.Run(ctx, q, nil); err != nil {
			return fmt.Errorf("constraint %q: %w", q, err)
		}
	}
	return nil
}

// CountEntitiesForRepo returns the :Entity count for repo, used to check the
// import against the input count.
//
// It counts via the repo's CONTAINS edges rather than the node repo property:
// shared nodes carry no repo property, so a property filter would under-report
// by the number of shared nodes. Every node gets a CONTAINS edge from the repo,
// so the edge count matches what this run wrote.
func (c *Client) CountEntitiesForRepo(ctx context.Context, repo string) (int, error) {
	session := c.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeRead})
	defer session.Close(ctx)
	res, err := session.Run(ctx, `MATCH (:Repository {name: $repo})-[:CONTAINS]->(n:Entity) RETURN count(DISTINCT n) AS c`, map[string]any{"repo": repo})
	if err != nil {
		return 0, fmt.Errorf("count entities: %w", err)
	}
	rec, err := res.Single(ctx)
	if err != nil {
		return 0, fmt.Errorf("count entities (read): %w", err)
	}
	c64, _ := rec.AsMap()["c"].(int64)
	return int(c64), nil
}

// MergeRepository ensures a (:Repository {name}) node exists.
func (c *Client) MergeRepository(ctx context.Context, repo string) error {
	session := c.Driver.NewSession(ctx, driver.SessionConfig{})
	defer session.Close(ctx)
	_, err := session.Run(ctx, `MERGE (:Repository {name: $name})`, map[string]any{"name": repo})
	return err
}

// ImportNodes imports all nodes in label-grouped UNWIND batches. commit/runID
// are stamped on each node for the sweep; pass "" to skip stamping. rewriteAll
// forces a full property rewrite on every node regardless of content hash -
// the --force repair path for manual property drift. Returns the graphify-ID
// to node_key map and per-label counts.
func (c *Client) ImportNodes(ctx context.Context, repo, commit, runID string, nodes []graphify.Node, rewriteAll bool) (map[string]string, map[string]int, error) {
	idToKey := make(map[string]string, len(nodes))
	labelGroups := make(map[string][]map[string]any)
	labelCounts := make(map[string]int)

	for _, n := range nodes {
		label := graphify.InferLabel(n)
		if !labelAllowlist[label] {
			label = "Symbol"
		}
		key := graphify.StableKey(repo, n)
		idToKey[n.ID] = key
		labelCounts[label]++

		// Shared nodes carry no repo property so the repo-scoped sweep never
		// deletes them; a nil map value makes `SET n += {}` skip the property.
		var repoProp, sharedProp any
		if graphify.IsShared(n) {
			sharedProp = true
		} else {
			repoProp = repo
		}

		row := map[string]any{
			"key":         key,
			"graphify_id": n.ID,
			"name":        n.Label,
			"norm_name":   n.NormLabel,
			// Pre-lowercased copies backing case-insensitive lookups.
			"name_lower":      orNil(strings.ToLower(n.Label)),
			"norm_name_lower": orNil(strings.ToLower(n.NormLabel)),
			"path":            n.SourceFile,
			"line":            n.SourceLocation,
			"language":        orNil(graphify.InferLanguage(n)),
			"file_type":       n.FileType,
			"community":       n.Community,
			"community_name":  n.CommunityName,
			"ecosystem":       orNil(n.Ecosystem),
			"repo":            repoProp,
			"shared":          sharedProp,
		}
		for _, k := range metadataProps {
			row[k] = nil
			if v, ok := n.Metadata[k]; ok {
				row[k] = v
			}
		}
		row["hash"] = rowContentHash(row, nodeProps)
		labelGroups[label] = append(labelGroups[label], row)
	}

	for label, rows := range labelGroups {
		for i := 0; i < len(rows); i += batchSize {
			end := i + batchSize
			if end > len(rows) {
				end = len(rows)
			}
			if err := c.importNodeBatch(ctx, label, repo, commit, runID, rows[i:end], rewriteAll); err != nil {
				return nil, nil, fmt.Errorf("import nodes (%s): %w", label, err)
			}
		}
	}

	return idToKey, labelCounts, nil
}

// rowContentHash hashes exactly the properties a batch write's SET map would
// apply, so a re-import with unchanged content can skip the rewrite. Keys are
// sorted so map build order never affects the hash; %v gives a stable string
// for the primitive/string/slice values graphify metadata actually produces.
// Sixteen hex chars (64 bits) is plenty to catch accidental drift - this
// isn't a security hash, just a cheap change detector.
func rowContentHash(row map[string]any, keys []string) string {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	h := sha256.New()
	for _, k := range sorted {
		fmt.Fprintf(h, "%s=%v\n", k, row[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// linkHashProps are the row keys hashed for the edge content-change check -
// the actual content fields, not the endpoints (which are the MERGE key) or
// provenance stamps (which always update).
var linkHashProps = []string{"weight", "confidence", "cs", "context"}

// ImportLinks imports all links in relation-grouped UNWIND batches. Each edge
// is stamped with repo (and commit/runID when set) so the sweep can scope edge
// deletion per repo. rewriteAll forces a full property rewrite regardless of
// content hash. Returns per-relation, skipped-unknown, and skipped-dangling
// counts.
func (c *Client) ImportLinks(ctx context.Context, repo, commit, runID string, links []graphify.Link, idToKey map[string]string, rewriteAll bool) (map[string]int, int, int, error) {
	relGroups := make(map[string][]map[string]any)
	relCounts := make(map[string]int)
	skippedUnknown := 0
	skippedDangling := 0

	for _, l := range links {
		rel, ok := graphify.MapRelation(l.Relation)
		if !ok {
			skippedUnknown++
			continue
		}
		srcKey, ok1 := idToKey[l.Source]
		tgtKey, ok2 := idToKey[l.Target]
		if !ok1 || !ok2 {
			skippedDangling++
			continue
		}
		relCounts[rel]++
		row := map[string]any{
			"s":          srcKey,
			"t":          tgtKey,
			"weight":     l.Weight,
			"confidence": l.Confidence,
			"cs":         l.ConfidenceScore,
			"context":    l.Context,
		}
		row["hash"] = rowContentHash(row, linkHashProps)
		relGroups[rel] = append(relGroups[rel], row)
	}

	for rel, rows := range relGroups {
		for i := 0; i < len(rows); i += batchSize {
			end := i + batchSize
			if end > len(rows) {
				end = len(rows)
			}
			if err := c.importLinkBatch(ctx, rel, repo, commit, runID, rows[i:end], rewriteAll); err != nil {
				return nil, 0, 0, fmt.Errorf("import links (%s): %w", rel, err)
			}
		}
	}

	return relCounts, skippedUnknown, skippedDangling, nil
}

// SweepStale removes repo nodes/edges this import run did not write, keeping
// the graph in sync with the source tree on re-index.
//
// Shared nodes have no repo property and aren't touched by the repo-scoped
// sweep; a final pass reaps any shared node left with no Entity edges. commit
// and runID must be non-empty, else the sweep would delete everything and is
// refused. Returns (nodesDeleted, relsDeleted).
func (c *Client) SweepStale(ctx context.Context, repo, commit, runID string) (int, int, error) {
	if commit == "" {
		return 0, 0, fmt.Errorf("sweep refused: commit is empty for repo %q", repo)
	}
	if runID == "" {
		// An empty run token would make every node "stale" and wipe the repo.
		return 0, 0, fmt.Errorf("sweep refused: runID is empty for repo %q", repo)
	}
	session := c.Driver.NewSession(ctx, driver.SessionConfig{})
	defer session.Close(ctx)

	runCount := func(query string, params map[string]any, what string) (int, error) {
		res, err := session.Run(ctx, query, params)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", what, err)
		}
		rec, err := res.Single(ctx)
		if err != nil {
			return 0, fmt.Errorf("%s (read): %w", what, err)
		}
		n, _ := rec.AsMap()["deleted"].(int64)
		return int(n), nil
	}
	params := map[string]any{"repo": repo, "commit": commit, "run": runID}

	// Staleness is keyed on last_run, not last_commit, so a same-commit re-index
	// still evicts what it didn't write. A missing last_run counts as stale.

	// Step 1: stale relationships. Edges are matched by their own repo stamp so
	// an edge to a shared node (which has no repo) is still swept; the
	// r.repo IS NULL branch covers edges from before edge stamping.
	relsDeleted, err := runCount(`
MATCH (a)-[r]->(b:Entity)
WHERE (r.repo = $repo OR (r.repo IS NULL AND a.repo = $repo))
  AND (r.last_run IS NULL OR r.last_run <> $run)
DELETE r
RETURN count(r) AS deleted`, params, "sweep stale relationships")
	if err != nil {
		return 0, 0, err
	}

	// Step 2: stale repo-owned nodes. Shared nodes never match (no repo).
	nodesDeleted, err := runCount(`
MATCH (n:Entity {repo: $repo})
WHERE (n.last_run IS NULL OR n.last_run <> $run)
  AND coalesce(n.shared, false) = false
DETACH DELETE n
RETURN count(n) AS deleted`, params, "sweep stale nodes")
	if err != nil {
		return 0, 0, err
	}

	// Step 3: reap shared nodes with no remaining Entity edges. CONTAINS edges
	// from a Repository don't count as references. This is an unscoped
	// full-graph scan, so only run it when this sweep actually deleted
	// something - in a single-writer world, a shared node can only become
	// orphaned as the direct result of a node or edge deletion, never by
	// itself. Skipping it on a no-op re-index avoids that scan cost every cycle.
	var orphans int
	if relsDeleted > 0 || nodesDeleted > 0 {
		orphans, err = runCount(`
MATCH (n:Entity)
WHERE n.shared = true AND NOT EXISTS { MATCH (n)--(:Entity) }
DETACH DELETE n
RETURN count(n) AS deleted`, map[string]any{}, "sweep orphaned shared nodes")
		if err != nil {
			return 0, 0, err
		}
	}

	return nodesDeleted + orphans, relsDeleted, nil
}

// VerifySweepClean counts repo-owned entities and repo-stamped relationships
// whose last_run doesn't match runID - i.e. exactly what SweepStale's queries
// target, but counted instead of deleted. A nonzero result means the sweep
// left stale data behind: either a bug in the sweep queries, or a concurrent
// writer touched the graph mid-run. Call this right after SweepStale.
func (c *Client) VerifySweepClean(ctx context.Context, repo, runID string) (staleNodes, staleRels int, err error) {
	session := c.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeRead})
	defer session.Close(ctx)

	count := func(query string) (int, error) {
		res, err := session.Run(ctx, query, map[string]any{"repo": repo, "run": runID})
		if err != nil {
			return 0, err
		}
		rec, err := res.Single(ctx)
		if err != nil {
			return 0, err
		}
		n, _ := rec.AsMap()["c"].(int64)
		return int(n), nil
	}

	staleNodes, err = count(`
MATCH (n:Entity {repo: $repo})
WHERE coalesce(n.shared, false) = false
  AND (n.last_run IS NULL OR n.last_run <> $run)
RETURN count(n) AS c`)
	if err != nil {
		return 0, 0, fmt.Errorf("verify sweep (nodes): %w", err)
	}

	staleRels, err = count(`
MATCH (a)-[r]->(b:Entity)
WHERE (r.repo = $repo OR (r.repo IS NULL AND a.repo = $repo))
  AND (r.last_run IS NULL OR r.last_run <> $run)
RETURN count(r) AS c`)
	if err != nil {
		return 0, 0, fmt.Errorf("verify sweep (relationships): %w", err)
	}

	return staleNodes, staleRels, nil
}

// nodeProps are the row keys importNodeBatch copies onto each node. They're
// internal constants, so interpolating them into the SET map is safe. A nil
// value removes the property (`SET n += {}` treats null as delete).
var nodeProps = append([]string{
	"graphify_id", "name", "norm_name", "name_lower", "norm_name_lower",
	"path", "line", "language",
	"file_type", "community", "community_name", "ecosystem", "repo", "shared",
}, metadataProps...)

// importNodeBatch runs one UNWIND batch for a single label.
//
// label is validated against labelAllowlist before reaching here, so
// interpolating it into the query string is safe. commit, if non-empty, is
// stamped on every node and CONTAINS edge as last_commit/last_run,
// unconditionally for every row in the batch - that stamp is what SweepStale
// keys staleness on, so it must never be skipped for a live row. An empty
// commit preserves legacy behavior for the static-graph importer CLI: no
// stamps, and always a full property rewrite (there's no content_hash
// baseline to gate against without a run to compare against).
//
// The full `SET n += {...}` (and content_hash refresh) only runs for rows
// whose hash differs from the stored one, or when rewriteAll forces it. The
// label set and CONTAINS edge stay unconditional so every row still gets
// those regardless of whether its properties were rewritten - a FOREACH/CASE
// gate is used instead of a WITH...WHERE filter so unchanged rows aren't
// dropped from the query's row stream before reaching the CONTAINS step.
func (c *Client) importNodeBatch(ctx context.Context, label, repo, commit, runID string, batch []map[string]any, rewriteAll bool) error {
	session := c.Driver.NewSession(ctx, driver.SessionConfig{})
	defer session.Close(ctx)

	setPairs := make([]string, 0, len(nodeProps))
	for _, p := range nodeProps {
		setPairs = append(setPairs, fmt.Sprintf("%s: row.%s", p, p))
	}

	stampClause := ""
	containsCommit := ""
	if commit != "" {
		stampClause = "SET n.last_commit = $commit, n.last_run = $run"
		containsCommit = ", c.last_commit = $commit, c.last_run = $run"
	}

	skipGate := "true"
	if commit != "" && !rewriteAll {
		skipGate = "coalesce(n.content_hash, '') <> row.hash"
	}

	query := fmt.Sprintf(`
MATCH (repo:Repository {name: $repo})
UNWIND $batch AS row
MERGE (n:Entity {node_key: row.key})
SET n:%s
%s
WITH repo, n, row, (%s) AS needsRewrite
FOREACH (_ IN CASE WHEN needsRewrite THEN [1] ELSE [] END |
  SET n += {
    %s,
    content_hash: row.hash
  }
)
MERGE (repo)-[c:CONTAINS]->(n)
SET c.repo = $repo%s`, label, stampClause, skipGate, strings.Join(setPairs, ",\n    "), containsCommit)

	params := map[string]any{"repo": repo, "batch": batch}
	if commit != "" {
		params["commit"] = commit
		params["run"] = runID
	}
	_, err := session.Run(ctx, query, params)
	return err
}

// importLinkBatch runs one UNWIND batch for a single relationship type.
// rel is validated via MapRelation's allowlist map before reaching here.
// repo and, when commit is non-empty, last_commit/last_run are stamped on
// every row unconditionally - same reasoning as importNodeBatch: those stamps
// are what SweepStale keys staleness on. The weight/confidence/context
// property rewrite is gated on content hash the same way, bypassed by
// rewriteAll or an empty commit (no baseline to compare against).
//
// MERGE keys on (source, type, target) only, so parallel edges collapse: two
// distinct call sites between the same pair become ONE edge, and the last
// row's weight/context wins. This is deliberate - the graph answers "does A
// call B", not "how many times" - but callers should not read weight as a
// call-site count.
func (c *Client) importLinkBatch(ctx context.Context, rel, repo, commit, runID string, batch []map[string]any, rewriteAll bool) error {
	session := c.Driver.NewSession(ctx, driver.SessionConfig{})
	defer session.Close(ctx)

	stampClause := ""
	if commit != "" {
		stampClause = "SET r.last_commit = $commit, r.last_run = $run"
	}

	skipGate := "true"
	if commit != "" && !rewriteAll {
		skipGate = "coalesce(r.content_hash, '') <> row.hash"
	}

	query := fmt.Sprintf(`
UNWIND $batch AS row
MATCH (a:Entity {node_key: row.s})
MATCH (b:Entity {node_key: row.t})
MERGE (a)-[r:%s]->(b)
SET r.repo = $repo
%s
WITH r, row, (%s) AS needsRewrite
FOREACH (_ IN CASE WHEN needsRewrite THEN [1] ELSE [] END |
  SET r += {
    weight:           row.weight,
    confidence:       row.confidence,
    confidence_score: row.cs,
    context:          row.context,
    content_hash:     row.hash
  }
)`, rel, stampClause, skipGate)

	params := map[string]any{"batch": batch, "repo": repo}
	if commit != "" {
		params["commit"] = commit
		params["run"] = runID
	}
	_, err := session.Run(ctx, query, params)
	return err
}
