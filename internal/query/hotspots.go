package query

import (
	"context"
	"sync"
	"time"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

const (
	hotspotDefaultLimit = 25
	hotspotMaxLimit     = 100

	// hotspotCacheTTL bounds staleness of the cached org-wide ranking. The
	// underlying data only changes when a repo re-indexes, so a few minutes
	// of staleness is invisible to users while making the page open-cheap
	// even with hundreds of repos: at most one full aggregation per TTL
	// window instead of one per visitor.
	hotspotCacheTTL = 5 * time.Minute
)

// hotspotCache memoizes the org-wide (unscoped) hotspot ranking per limit.
// Scoped queries are cheap (index on n.repo) and are not cached.
type hotspotCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[int]hotspotCacheEntry
	now     func() time.Time // injectable for tests
}

type hotspotCacheEntry struct {
	at    time.Time
	nodes []HotspotNode
}

func newHotspotCache(ttl time.Duration) *hotspotCache {
	return &hotspotCache{ttl: ttl, entries: map[int]hotspotCacheEntry{}, now: time.Now}
}

func (c *hotspotCache) get(limit int) ([]HotspotNode, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[limit]
	if !ok || c.now().Sub(e.at) > c.ttl {
		return nil, false
	}
	return e.nodes, true
}

func (c *hotspotCache) put(limit int, nodes []HotspotNode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[limit] = hotspotCacheEntry{at: c.now(), nodes: nodes}
}

// hotspotRelations are the dependency-bearing relationship types counted for
// fan-in. Structural relations (CONTAINS, HAS_METHOD, DECLARES, IN_SCHEMA,
// EXPOSES_ROUTE, SCHEDULED) are excluded: a file "containing" fifty functions
// is not fifty pieces of code depending on it.
var hotspotRelations = []string{
	"CALLS", "REFERENCES", "EMBEDS", "HANDLED_BY",
	"DEPENDS_ON", "DEPENDS_ON_REPO",
	"PRODUCES", "CONSUMES",
	"READS_TABLE", "WRITES_TABLE", "TRIGGERS_ON", "DEPENDS_ON_OBJECT",
	"READS_SOURCE", "WRITES_DESTINATION",
}

// HotspotNode is one high-fan-in entity: code that many other pieces of code
// depend on, and therefore a high-risk change site (UC-7).
type HotspotNode struct {
	Name   string   `json:"name"`
	Repo   string   `json:"repo"`
	Path   string   `json:"path"`
	Line   string   `json:"line"`
	Labels []string `json:"labels"`
	// FanIn is the number of incoming dependency edges from other entities.
	FanIn int `json:"fan_in"`
	// DependentRepos counts the distinct repositories those edges come from -
	// a cross-repo hotspot (>1) is riskier than a popular local helper.
	DependentRepos int `json:"dependent_repos"`
}

// FindHotspots returns entities ranked by incoming dependency fan-in,
// optionally scoped to repositories (repos empty means org-wide). limit <= 0
// uses the default; values above the cap are clamped. Org-wide results are
// served from a short-TTL cache because they aggregate every dependency
// edge in the graph.
func (s *Service) FindHotspots(ctx context.Context, repos []string, limit int) ([]HotspotNode, error) {
	if limit <= 0 {
		limit = hotspotDefaultLimit
	}
	if limit > hotspotMaxLimit {
		limit = hotspotMaxLimit
	}
	orgWide := len(repos) == 0
	if orgWide {
		if nodes, ok := s.hotspots.get(limit); ok {
			return nodes, nil
		}
	}

	const cypher = `
MATCH (n:Entity)<-[r]-(m:Entity)
WHERE type(r) IN $rels
  AND (size($repos) = 0 OR n.repo IN $repos)
  AND m <> n
WITH n, count(r) AS fan_in, count(DISTINCT r.repo) AS dependent_repos
ORDER BY fan_in DESC, n.name
LIMIT $limit
RETURN n.name    AS name,
       n.repo    AS repo,
       n.path    AS path,
       n.line    AS line,
       labels(n) AS labels,
       fan_in,
       dependent_repos
`

	out, err := s.read(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, map[string]any{
			"rels": hotspotRelations, "repos": orEmpty(repos), "limit": limit,
		})
		if err != nil {
			return nil, err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		nodes := make([]HotspotNode, 0, len(records))
		for _, r := range records {
			m := r.AsMap()
			nodes = append(nodes, HotspotNode{
				Name:           asString(m["name"]),
				Repo:           asString(m["repo"]),
				Path:           asString(m["path"]),
				Line:           asString(m["line"]),
				Labels:         asStringSlice(m["labels"]),
				FanIn:          asInt(m["fan_in"]),
				DependentRepos: asInt(m["dependent_repos"]),
			})
		}
		return nodes, nil
	})
	if err != nil {
		return nil, err
	}
	nodes := out.([]HotspotNode)
	if orgWide {
		s.hotspots.put(limit, nodes)
	}
	return nodes, nil
}
