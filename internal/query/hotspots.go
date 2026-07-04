package query

import (
	"context"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

const (
	hotspotDefaultLimit = 25
	hotspotMaxLimit     = 100
)

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
	// DependentRepos counts the distinct repositories those edges come from —
	// a cross-repo hotspot (>1) is riskier than a popular local helper.
	DependentRepos int `json:"dependent_repos"`
}

// FindHotspots returns entities ranked by incoming dependency fan-in,
// optionally scoped to one repo (repo="" means org-wide). limit <= 0 uses
// the default; values above the cap are clamped.
func (s *Service) FindHotspots(ctx context.Context, repo string, limit int) ([]HotspotNode, error) {
	if limit <= 0 {
		limit = hotspotDefaultLimit
	}
	if limit > hotspotMaxLimit {
		limit = hotspotMaxLimit
	}

	const cypher = `
MATCH (n:Entity)<-[r]-(m:Entity)
WHERE type(r) IN $rels
  AND ($repo = '' OR n.repo = $repo)
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
			"rels": hotspotRelations, "repo": repo, "limit": limit,
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
	return out.([]HotspotNode), nil
}
