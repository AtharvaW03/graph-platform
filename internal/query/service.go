package query

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"graph-platform/internal/neo4j"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

const (
	defaultBlastDepth   = 3
	maxBlastDepth       = 10
	shortestPathHopsMax = 15
	searchLimit         = 100

	// symbolLimit caps result sets so a hub symbol can't return tens of
	// thousands of rows.
	symbolLimit = 500

	// txTimeout bounds every read transaction server-side so a runaway
	// traversal can't pin the database.
	txTimeout = 30 * time.Second
)

type Service struct {
	db *neo4j.Client
	// hotspots caches the org-wide hotspot ranking; see hotspots.go.
	hotspots *hotspotCache
}

func NewService(db *neo4j.Client) *Service {
	return &Service{db: db, hotspots: newHotspotCache(hotspotCacheTTL)}
}

func (s *Service) read(ctx context.Context, fn func(tx driver.ManagedTransaction) (any, error)) (any, error) {
	sess := s.db.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeRead})
	defer sess.Close(ctx)
	return sess.ExecuteRead(ctx, fn, driver.WithTxTimeout(txTimeout))
}

// Search returns nodes whose name or norm_name contains q (case-insensitive),
// ordered by match quality (exact > prefix > contains) then name length. It
// matches the indexed name_lower/norm_name_lower columns; the term is
// lowercased here. repos, when non-empty, scopes to those repos - shared nodes
// carry no repo and drop out of scoped results.
func (s *Service) Search(ctx context.Context, q string, repos []string) ([]SearchResult, error) {
	if q == "" {
		return []SearchResult{}, nil
	}

	const cypher = `
MATCH (n:Entity)
WHERE (n.name_lower CONTAINS $q OR n.norm_name_lower CONTAINS $q)
  AND (size($repos) = 0 OR n.repo IN $repos)
RETURN n.node_key      AS node_key,
       n.graphify_id   AS graphify_id,
       n.name          AS name,
       labels(n)       AS labels,
       n.repo          AS repo,
       n.path          AS path,
       n.line          AS line,
       CASE
         WHEN n.name_lower = $q                  THEN 0
         WHEN n.name_lower STARTS WITH $q        THEN 1
         WHEN n.norm_name_lower = $q             THEN 2
         WHEN n.norm_name_lower STARTS WITH $q   THEN 3
         ELSE 4
       END AS rank
ORDER BY rank, size(n.name), n.name
LIMIT $limit
`

	out, err := s.read(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, map[string]any{"q": strings.ToLower(q), "limit": searchLimit, "repos": orEmpty(repos)})
		if err != nil {
			return nil, err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		results := make([]SearchResult, 0, len(records))
		for _, r := range records {
			results = append(results, SearchResult{
				NodeKey:    asString(r.AsMap()["node_key"]),
				GraphifyID: asString(r.AsMap()["graphify_id"]),
				Name:       asString(r.AsMap()["name"]),
				Labels:     asStringSlice(r.AsMap()["labels"]),
				Repo:       asString(r.AsMap()["repo"]),
				Path:       asString(r.AsMap()["path"]),
				Line:       asString(r.AsMap()["line"]),
			})
		}
		return results, nil
	})
	if err != nil {
		return nil, err
	}
	return out.([]SearchResult), nil
}

// FindSymbol returns every node whose name (or norm_name) exactly matches the
// supplied symbol. Case-insensitive; repos non-empty scopes the match.
func (s *Service) FindSymbol(ctx context.Context, symbol string, repos []string) ([]SymbolResult, error) {
	if symbol == "" {
		return []SymbolResult{}, nil
	}

	const cypher = `
MATCH (n:Entity)
WHERE (n.name_lower = $s OR n.norm_name_lower = $s)
  AND (size($repos) = 0 OR n.repo IN $repos)
RETURN n.name           AS name,
       n.repo           AS repo,
       n.path           AS path,
       n.line           AS line,
       labels(n)        AS labels,
       n.community      AS community
ORDER BY n.repo, n.path, n.line
LIMIT $limit
`

	out, err := s.read(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, map[string]any{"s": strings.ToLower(symbol), "limit": symbolLimit, "repos": orEmpty(repos)})
		if err != nil {
			return nil, err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		results := make([]SymbolResult, 0, len(records))
		for _, r := range records {
			m := r.AsMap()
			results = append(results, SymbolResult{
				Name:      asString(m["name"]),
				Repo:      asString(m["repo"]),
				Path:      asString(m["path"]),
				Line:      asString(m["line"]),
				Labels:    asStringSlice(m["labels"]),
				Community: asInt(m["community"]),
			})
		}
		return results, nil
	})
	if err != nil {
		return nil, err
	}
	return out.([]SymbolResult), nil
}

// FindCallers returns every function with a CALLS edge into the symbol. repos
// non-empty scopes to callees in those repos.
func (s *Service) FindCallers(ctx context.Context, symbol string, repos []string) ([]CallEdge, error) {
	if symbol == "" {
		return []CallEdge{}, nil
	}

	const cypher = `
MATCH (caller:Entity)-[:CALLS]->(callee:Entity)
WHERE (callee.name_lower = $s OR callee.norm_name_lower = $s)
  AND (size($repos) = 0 OR callee.repo IN $repos)
RETURN caller.name AS caller,
       caller.repo AS caller_repo,
       caller.path AS caller_path,
       caller.line AS caller_line,
       labels(caller) AS labels,
       callee.name AS callee,
       callee.repo AS callee_repo,
       callee.path AS callee_path
ORDER BY caller.repo, caller.path, caller.line
LIMIT $limit
`
	return s.runCallEdgeQuery(ctx, cypher, symbol, repos)
}

// FindCallees returns every function the supplied symbol calls. repos
// non-empty scopes to callers defined in those repos.
func (s *Service) FindCallees(ctx context.Context, symbol string, repos []string) ([]CallEdge, error) {
	if symbol == "" {
		return []CallEdge{}, nil
	}

	const cypher = `
MATCH (caller:Entity)-[:CALLS]->(callee:Entity)
WHERE (caller.name_lower = $s OR caller.norm_name_lower = $s)
  AND (size($repos) = 0 OR caller.repo IN $repos)
WITH caller, callee
ORDER BY callee.repo, callee.path, callee.line
RETURN caller.name AS caller,
       caller.repo AS caller_repo,
       caller.path AS caller_path,
       caller.line AS caller_line,
       labels(callee) AS labels,
       callee.name AS callee,
       callee.repo AS callee_repo,
       callee.path AS callee_path
LIMIT $limit
`
	return s.runCallEdgeQuery(ctx, cypher, symbol, repos)
}

func (s *Service) runCallEdgeQuery(ctx context.Context, cypher, symbol string, repos []string) ([]CallEdge, error) {
	out, err := s.read(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, map[string]any{"s": strings.ToLower(symbol), "limit": symbolLimit, "repos": orEmpty(repos)})
		if err != nil {
			return nil, err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		edges := make([]CallEdge, 0, len(records))
		for _, r := range records {
			m := r.AsMap()
			edges = append(edges, CallEdge{
				Caller:     asString(m["caller"]),
				CallerRepo: asString(m["caller_repo"]),
				CallerPath: asString(m["caller_path"]),
				CallerLine: asString(m["caller_line"]),
				Callee:     asString(m["callee"]),
				CalleeRepo: asString(m["callee_repo"]),
				CalleePath: asString(m["callee_path"]),
				Labels:     asStringSlice(m["labels"]),
			})
		}
		return edges, nil
	})
	if err != nil {
		return nil, err
	}
	return out.([]CallEdge), nil
}

// BlastRadius walks outgoing edges up to depth and returns each reachable node
// with its minimum distance from the source symbol, ordered by distance asc.
// depth <= 0 falls back to defaultBlastDepth; depths above maxBlastDepth are
// clamped to prevent runaway traversals. repos non-empty scopes the START
// symbol only - reachable nodes may cross repo boundaries by design.
func (s *Service) BlastRadius(ctx context.Context, symbol string, depth int, repos []string) ([]ImpactNode, error) {
	if symbol == "" {
		return []ImpactNode{}, nil
	}
	if depth <= 0 {
		depth = defaultBlastDepth
	}
	if depth > maxBlastDepth {
		depth = maxBlastDepth
	}

	// depth can't be a Cypher parameter; it's clamped above, so formatting it
	// in is safe. Aggregating to distinct nodes with min(length(p)) makes the
	// planner use a pruning BFS rather than enumerate every path.
	cypher := fmt.Sprintf(`
MATCH (start:Entity)
WHERE (start.name_lower = $s OR start.norm_name_lower = $s)
  AND (size($repos) = 0 OR start.repo IN $repos)
MATCH p = (start)-[*1..%d]->(impacted:Entity)
WITH impacted, min(length(p)) AS distance
RETURN impacted.name  AS name,
       impacted.repo  AS repo,
       impacted.path  AS path,
       impacted.line  AS line,
       labels(impacted) AS labels,
       distance
ORDER BY distance, name
LIMIT $limit
`, depth)

	out, err := s.read(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, map[string]any{"s": strings.ToLower(symbol), "limit": symbolLimit, "repos": orEmpty(repos)})
		if err != nil {
			return nil, err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		nodes := make([]ImpactNode, 0, len(records))
		for _, r := range records {
			m := r.AsMap()
			nodes = append(nodes, ImpactNode{
				Name:     asString(m["name"]),
				Repo:     asString(m["repo"]),
				Path:     asString(m["path"]),
				Line:     asString(m["line"]),
				Labels:   asStringSlice(m["labels"]),
				Distance: asInt(m["distance"]),
			})
		}
		return nodes, nil
	})
	if err != nil {
		return nil, err
	}
	return out.([]ImpactNode), nil
}

// ShortestPath returns one shortest undirected path between source and target
// symbols, as an ordered list of nodes. Each node carries the relationship
// type used to reach it from the previous node (empty on the first).
// repos non-empty scopes which nodes can anchor the endpoints; the path
// itself may traverse any repo.
func (s *Service) ShortestPath(ctx context.Context, source, target string, repos []string) ([]PathNode, error) {
	if source == "" || target == "" {
		return []PathNode{}, nil
	}

	cypher := fmt.Sprintf(`
MATCH (src:Entity), (dst:Entity)
WHERE (src.name_lower = $src OR src.norm_name_lower = $src)
  AND (dst.name_lower = $dst OR dst.norm_name_lower = $dst)
  AND (size($repos) = 0 OR (src.repo IN $repos AND dst.repo IN $repos))
WITH src, dst LIMIT 1
MATCH p = shortestPath((src)-[*..%d]-(dst))
WITH nodes(p) AS ns, relationships(p) AS rs
UNWIND range(0, size(ns)-1) AS i
RETURN ns[i].name  AS name,
       ns[i].repo  AS repo,
       ns[i].path  AS path,
       labels(ns[i]) AS labels,
       CASE WHEN i = 0 THEN '' ELSE type(rs[i-1]) END AS relationship,
       i AS idx
ORDER BY idx
`, shortestPathHopsMax)

	out, err := s.read(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, map[string]any{"src": strings.ToLower(source), "dst": strings.ToLower(target), "repos": orEmpty(repos)})
		if err != nil {
			return nil, err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		path := make([]PathNode, 0, len(records))
		for _, r := range records {
			m := r.AsMap()
			path = append(path, PathNode{
				Name:         asString(m["name"]),
				Repo:         asString(m["repo"]),
				Path:         asString(m["path"]),
				Labels:       asStringSlice(m["labels"]),
				Relationship: asString(m["relationship"]),
			})
		}
		return path, nil
	})
	if err != nil {
		return nil, err
	}
	return out.([]PathNode), nil
}

// ErrNotImplemented is returned by stubs that depend on data the importer
// doesn't yet produce.
var ErrNotImplemented = errors.New("not implemented")

// FindRepositoryDependencies will return cross-repo dependency edges once the
// importer emits them; stub for now.
func (s *Service) FindRepositoryDependencies(ctx context.Context, repo string) ([]SymbolResult, error) {
	return nil, ErrNotImplemented
}

// orEmpty normalizes a nil repo filter to an empty slice for the driver.
func orEmpty(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func asInt(v any) int {
	switch x := v.(type) {
	case int64:
		return int(x)
	case int:
		return x
	case float64:
		return int(x)
	}
	return 0
}

func asStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
