package query

import (
	"context"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// RepoInfo is one indexed repository, as served by GET /repos.
type RepoInfo struct {
	Name  string `json:"name"`
	Nodes int    `json:"nodes"`
}

// ListRepositories returns every (:Repository) node with its contained
// entity count, feeding the web UI's repo-scope dropdown.
func (s *Service) ListRepositories(ctx context.Context) ([]RepoInfo, error) {
	const cypher = `
MATCH (r:Repository)
RETURN r.name AS name,
       count { (r)-[:CONTAINS]->(:Entity) } AS nodes
ORDER BY name
`
	out, err := s.read(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, nil)
		if err != nil {
			return nil, err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		repos := make([]RepoInfo, 0, len(records))
		for _, r := range records {
			m := r.AsMap()
			repos = append(repos, RepoInfo{
				Name:  asString(m["name"]),
				Nodes: asInt(m["nodes"]),
			})
		}
		return repos, nil
	})
	if err != nil {
		return nil, err
	}
	return out.([]RepoInfo), nil
}
