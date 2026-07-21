package neo4j

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"a1-knowledge-graph/internal/graphify"

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

// removeAllLabelsClause is a static `REMOVE n:A:B:C...` over every allowlisted
// label, built once. importNodeBatch runs it before re-adding a node's
// current label so a kind change (Function -> Class, say) replaces the label
// instead of accumulating both - StableKey doesn't change when a node's kind
// does, so the node itself is never re-created, only re-labeled. :Entity is
// never in labelAllowlist, so it's never removed.
var removeAllLabelsClause = buildRemoveAllLabelsClause()

func buildRemoveAllLabelsClause() string {
	names := make([]string, 0, len(labelAllowlist))
	for l := range labelAllowlist {
		names = append(names, l)
	}
	sort.Strings(names)
	return "REMOVE n:" + strings.Join(names, ":")
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
// constraints, the repo index, TEXT indexes on the pre-lowercased name
// columns, and the entity_search fulltext index. All idempotent.
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
		// Backs Service.Search's fulltext tier (token/stem matching, relevance
		// scoring) ahead of the exact/prefix/CONTAINS fallback below it.
		`CREATE FULLTEXT INDEX entity_search IF NOT EXISTS FOR (n:Entity) ON EACH [n.name, n.norm_name, n.path]`,
	}
	for _, q := range stmts {
		if _, err := session.Run(ctx, q, nil); err != nil {
			// IF NOT EXISTS is not atomic across concurrent creators: two
			// clients booting against a fresh database (indexer +
			// query-service, or parallel test packages) can both pass the
			// existence check and one loses with "equivalent already exists".
			// That loser's desired state is satisfied - treat it as success.
			var neoErr *driver.Neo4jError
			if errors.As(err, &neoErr) && neoErr.Code == "Neo.ClientError.Schema.EquivalentSchemaRuleAlreadyExists" {
				continue
			}
			return fmt.Errorf("constraint %q: %w", q, err)
		}
	}

	// Schema commands above return as soon as the index/constraint is
	// created, not once it's populated. Waiting here (idempotent, cheap once
	// everything is already online) means a freshly created entity_search
	// index can't be queried by Service.Search before it has any data in it,
	// which would look like every search silently falling back to CONTAINS.
	if _, err := session.Run(ctx, `CALL db.awaitIndexes()`, nil); err != nil {
		return fmt.Errorf("await indexes: %w", err)
	}
	return nil
}

// CountEntitiesForRepo returns the :Entity count for repo, used to check the
// import against the input count.
//
// It counts via the repo's HAS_ENTITY edges rather than the node repo
// property: shared nodes carry no repo property, so a property filter would
// under-report by the number of shared nodes. Every node gets a HAS_ENTITY
// edge from the repo, so the edge count matches what this run wrote.
//
// HAS_ENTITY is the platform's own repo-ownership edge, separate from
// graphify's structural CONTAINS (file contains function, etc) - the two used
// to share the CONTAINS type, which let shortestPath traversals route through
// the Repository hub. See ImportNodes/importNodeBatch.
func (c *Client) CountEntitiesForRepo(ctx context.Context, repo string) (int, error) {
	session := c.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeRead})
	defer session.Close(ctx)
	res, err := session.Run(ctx, `MATCH (:Repository {name: $repo})-[:HAS_ENTITY]->(n:Entity) RETURN count(DISTINCT n) AS c`, map[string]any{"repo": repo})
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

// StampRepoSync records on the (:Repository) node when the repo was last
// checked against its remote (last_synced_at) and, when indexed is true,
// when its content was last imported (last_indexed_at). Backs the
// query-service /freshness endpoint.
func (c *Client) StampRepoSync(ctx context.Context, repo string, indexed bool) error {
	session := c.Driver.NewSession(ctx, driver.SessionConfig{})
	defer session.Close(ctx)
	cypher := `MERGE (r:Repository {name: $name}) SET r.last_synced_at = datetime()`
	if indexed {
		cypher += `, r.last_indexed_at = datetime()`
	}
	_, err := session.Run(ctx, cypher, map[string]any{"name": repo})
	return err
}

// ImportNodes imports all nodes in label-grouped UNWIND batches. commit/runID
// are stamped on each node for the sweep; pass "" to skip stamping. rewriteAll
// forces a full property rewrite on every node regardless of content hash -
// the --force repair path for manual property drift. Returns the graphify-ID
// to node_key map, a node_key->shared map (true only for shared nodes, used
// by ImportLinks to detect shared-shared edges), and per-label counts.
func (c *Client) ImportNodes(ctx context.Context, repo, commit, runID string, nodes []graphify.Node, rewriteAll bool) (map[string]string, map[string]bool, map[string]int, error) {
	idToKey := make(map[string]string, len(nodes))
	sharedKeys := make(map[string]bool, len(nodes))
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
			sharedKeys[key] = true
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
		// label is hashed (so a kind change invalidates the rewrite gate, see
		// importNodeBatch) but is not a node property - it must never be added
		// to nodeProps/setPairs, or `SET n += {...}` would create a literal
		// "label" property instead of a Neo4j label.
		row["label"] = label
		row["hash"] = rowContentHash(row, nodeHashProps)
		labelGroups[label] = append(labelGroups[label], row)
	}

	for label, rows := range labelGroups {
		for i := 0; i < len(rows); i += batchSize {
			end := i + batchSize
			if end > len(rows) {
				end = len(rows)
			}
			if err := c.importNodeBatch(ctx, label, repo, commit, runID, rows[i:end], rewriteAll); err != nil {
				return nil, nil, nil, fmt.Errorf("import nodes (%s): %w", label, err)
			}
		}
	}

	return idToKey, sharedKeys, labelCounts, nil
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
//
// sharedKeys (from ImportNodes) marks which endpoints are shared. When BOTH
// endpoints of an edge are shared (e.g. two SQL objects both org-global),
// repo is folded into the MERGE key so each repo that emits the edge gets its
// own parallel relationship instance instead of all repos fighting over one
// shared instance's repo/last_run stamps - see importLinkBatch. An edge with
// at least one repo-owned endpoint keeps today's shape unchanged; that
// endpoint's own repo-scoping already makes the edge repo-specific.
func (c *Client) ImportLinks(ctx context.Context, repo, commit, runID string, links []graphify.Link, idToKey map[string]string, sharedKeys map[string]bool, rewriteAll bool) (map[string]int, int, int, error) {
	relGroups := make(map[string][]map[string]any)
	sharedRelGroups := make(map[string][]map[string]any)
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
		if sharedKeys[srcKey] && sharedKeys[tgtKey] {
			sharedRelGroups[rel] = append(sharedRelGroups[rel], row)
		} else {
			relGroups[rel] = append(relGroups[rel], row)
		}
	}

	importGroups := func(groups map[string][]map[string]any, sharedShared bool) error {
		for rel, rows := range groups {
			for i := 0; i < len(rows); i += batchSize {
				end := i + batchSize
				if end > len(rows) {
					end = len(rows)
				}
				if err := c.importLinkBatch(ctx, rel, repo, commit, runID, rows[i:end], rewriteAll, sharedShared); err != nil {
					return fmt.Errorf("import links (%s): %w", rel, err)
				}
			}
		}
		return nil
	}
	if err := importGroups(relGroups, false); err != nil {
		return nil, 0, 0, err
	}
	if err := importGroups(sharedRelGroups, true); err != nil {
		return nil, 0, 0, err
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

	// Step 3: reap shared nodes with no remaining Entity edges. HAS_ENTITY
	// edges from a Repository don't count as references. This is an unscoped
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

// ListRepositoryNames returns every repository name present in the graph:
// (:Repository) nodes plus any distinct Entity repo property. The union
// matters for retirement reconciliation: a manual `DETACH DELETE` of just
// the Repository node (a tempting one-liner) strands the repo's entities
// with no Repository row, and a Repository-only listing would never see
// them again. The n.repo scan is backed by the entity_repo index.
func (c *Client) ListRepositoryNames(ctx context.Context) ([]string, error) {
	session := c.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeRead})
	defer session.Close(ctx)
	res, err := session.Run(ctx, `
MATCH (r:Repository) RETURN r.name AS name
UNION
MATCH (n:Entity) WHERE n.repo IS NOT NULL RETURN DISTINCT n.repo AS name`, nil)
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	records, err := res.Collect(ctx)
	if err != nil {
		return nil, fmt.Errorf("list repositories (read): %w", err)
	}
	names := make([]string, 0, len(records))
	for _, rec := range records {
		if name, ok := rec.AsMap()["name"].(string); ok && name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// DeleteRepositoryGraph removes a retired repository from the graph: its
// repo-stamped relationships, its repo-owned entities, the Repository node
// itself, and finally any shared nodes the deletion orphaned. The steps
// mirror SweepStale's, minus the last_run staleness filter - retirement
// means everything the repo owns is stale. Returns (nodes, rels) deleted.
func (c *Client) DeleteRepositoryGraph(ctx context.Context, repo string) (int, int, error) {
	if repo == "" {
		// An empty name would make the repo-stamped queries match nothing and
		// the caller think retirement succeeded; refuse loudly instead.
		return 0, 0, fmt.Errorf("delete repository refused: empty repo name")
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
	params := map[string]any{"repo": repo}

	// Step 1: every relationship stamped with this repo. Covers shared-shared
	// parallel edges, whose endpoints are not repo-owned and so survive step 2.
	relsDeleted, err := runCount(`
MATCH ()-[r {repo: $repo}]->()
DELETE r
RETURN count(r) AS deleted`, params, "delete repo relationships")
	if err != nil {
		return 0, 0, err
	}

	// Step 2: repo-owned entities. DETACH also removes unstamped legacy edges.
	nodesDeleted, err := runCount(`
MATCH (n:Entity {repo: $repo})
DETACH DELETE n
RETURN count(n) AS deleted`, params, "delete repo entities")
	if err != nil {
		return 0, 0, err
	}

	// Step 3: the Repository node. Must precede the orphan reap so its
	// HAS_ENTITY edges to shared nodes are gone before orphanhood is judged.
	if _, err := runCount(`
MATCH (rep:Repository {name: $repo})
DETACH DELETE rep
RETURN count(rep) AS deleted`, params, "delete repository node"); err != nil {
		return 0, 0, err
	}

	// Step 4: shared nodes orphaned by the above - same rule as SweepStale's
	// final pass: no remaining edge to any Entity means no repo references it.
	orphans, err := runCount(`
MATCH (n:Entity)
WHERE n.shared = true AND NOT EXISTS { MATCH (n)--(:Entity) }
DETACH DELETE n
RETURN count(n) AS deleted`, map[string]any{}, "reap orphaned shared nodes")
	if err != nil {
		return 0, 0, err
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

// nodeHashProps is nodeProps plus "label" - the content hash must reflect a
// kind change (Function -> Class) even though "label" is never one of the SET
// properties. Keeping this list separate from nodeProps is what keeps a
// Neo4j label from also being written as a literal node property.
var nodeHashProps = append(append([]string(nil), nodeProps...), "label")

// importNodeBatch runs one UNWIND batch for a single label.
//
// label is validated against labelAllowlist before reaching here, so
// interpolating it into the query string is safe. commit, if non-empty, is
// stamped on every node and HAS_ENTITY edge as last_commit/last_run,
// unconditionally for every row in the batch - that stamp is what SweepStale
// keys staleness on, so it must never be skipped for a live row. An empty
// commit (the static-graph importer CLI) means no stamps and always a full
// property rewrite.
//
// The full property rewrite (SET n += {...}, content_hash refresh) only runs
// for rows whose hash differs from the stored one, or when rewriteAll forces
// it. Label maintenance (REMOVE every allowlisted label, then SET the
// current one) runs inside that same gate: since row.hash includes the
// current label (see ImportNodes), a kind change always trips the gate, so
// REMOVE+SET always fires when the label actually needs to change - the node
// never accumulates a stale label from a prior kind. The HAS_ENTITY edge
// stays unconditional so every row still gets it regardless of whether its
// properties were rewritten - a FOREACH/CASE gate is used instead of a
// WITH...WHERE filter so unchanged rows aren't dropped from the query's row
// stream before reaching the HAS_ENTITY step.
//
// HAS_ENTITY is the platform's repo-ownership edge, deliberately not named
// CONTAINS: graphify's own AST relation ("file contains function") uses
// CONTAINS too, and sharing one type let path queries route through the
// Repository hub as if it were a real 2-hop structural relationship.
func (c *Client) importNodeBatch(ctx context.Context, label, repo, commit, runID string, batch []map[string]any, rewriteAll bool) error {
	session := c.Driver.NewSession(ctx, driver.SessionConfig{})
	defer session.Close(ctx)

	setPairs := make([]string, 0, len(nodeProps))
	for _, p := range nodeProps {
		setPairs = append(setPairs, fmt.Sprintf("%s: row.%s", p, p))
	}

	stampClause := ""
	hasEntityCommit := ""
	if commit != "" {
		stampClause = "SET n.last_commit = $commit, n.last_run = $run"
		hasEntityCommit = ", c.last_commit = $commit, c.last_run = $run"
	}

	skipGate := "true"
	if commit != "" && !rewriteAll {
		skipGate = "coalesce(n.content_hash, '') <> row.hash"
	}

	query := fmt.Sprintf(`
MATCH (repo:Repository {name: $repo})
UNWIND $batch AS row
MERGE (n:Entity {node_key: row.key})
%s
WITH repo, n, row, (%s) AS needsRewrite
FOREACH (_ IN CASE WHEN needsRewrite THEN [1] ELSE [] END |
  %s
  SET n:%s
  SET n += {
    %s,
    content_hash: row.hash
  }
)
MERGE (repo)-[c:HAS_ENTITY]->(n)
SET c.repo = $repo%s`, stampClause, skipGate, removeAllLabelsClause, label, strings.Join(setPairs, ",\n    "), hasEntityCommit)

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
// commit, when non-empty, stamps last_commit/last_run on every row
// unconditionally - same reasoning as importNodeBatch: those stamps are what
// SweepStale keys staleness on. The weight/confidence/context property
// rewrite is gated on content hash the same way, bypassed by rewriteAll or an
// empty commit (no baseline to compare against).
//
// MERGE keys on (source, type, target) only, so parallel edges collapse: two
// distinct call sites between the same pair become ONE edge, and the last
// row's weight/context wins. This is deliberate - the graph answers "does A
// call B", not "how many times" - but callers should not read weight as a
// call-site count.
//
// sharedShared changes that collapsing for one specific case: an edge whose
// BOTH endpoints are shared nodes (e.g. two org-global SQL objects). Without
// it, two repos independently emitting the same shared-to-shared edge fight
// over one relationship's repo/last_run stamps - whichever repo imports last
// "owns" it, and that repo's sweep can delete the edge out from under the
// other repo's still-valid reference. With it, repo is folded into the MERGE
// key, so each repo gets its own parallel edge instance, each with its own
// stamps, so one repo's sweep only ever touches its own instance. An edge
// with at least one repo-owned endpoint doesn't need this: that endpoint's
// own repo scoping already makes the edge unambiguous.
func (c *Client) importLinkBatch(ctx context.Context, rel, repo, commit, runID string, batch []map[string]any, rewriteAll, sharedShared bool) error {
	session := c.Driver.NewSession(ctx, driver.SessionConfig{})
	defer session.Close(ctx)

	mergeClause := fmt.Sprintf("MERGE (a)-[r:%s]->(b)\nSET r.repo = $repo", rel)
	if sharedShared {
		// repo is part of the merge pattern itself, not a separate SET: MERGE
		// only matches an existing edge whose repo already equals this one, so
		// a different repo's edge is left untouched and a new parallel edge is
		// created instead of being overwritten.
		mergeClause = fmt.Sprintf("MERGE (a)-[r:%s {repo: $repo}]->(b)", rel)
	}

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
%s
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
)`, mergeClause, stampClause, skipGate)

	params := map[string]any{"batch": batch, "repo": repo}
	if commit != "" {
		params["commit"] = commit
		params["run"] = runID
	}
	_, err := session.Run(ctx, query, params)
	return err
}
