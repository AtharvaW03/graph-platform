package query

import (
	"context"
	"time"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// RepoFreshness is one repository's freshness record: when the indexer last
// checked it against its remote, and when its content was last imported.
type RepoFreshness struct {
	Repo          string
	LastSyncedAt  time.Time
	LastIndexedAt time.Time // zero when never imported
}

// Freshness returns every repository's freshness stamps, oldest-checked
// first. Repositories without a stamp (never processed by a stamping
// indexer) are omitted.
func (s *Service) Freshness(ctx context.Context) ([]RepoFreshness, error) {
	const cypher = `
MATCH (r:Repository)
WHERE r.last_synced_at IS NOT NULL
RETURN r.name AS repo, r.last_synced_at AS synced, r.last_indexed_at AS indexed
ORDER BY synced ASC
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
		rows := make([]RepoFreshness, 0, len(records))
		for _, r := range records {
			m := r.AsMap()
			rows = append(rows, RepoFreshness{
				Repo:          asString(m["repo"]),
				LastSyncedAt:  asTime(m["synced"]),
				LastIndexedAt: asTime(m["indexed"]),
			})
		}
		return rows, nil
	})
	if err != nil {
		return nil, err
	}
	return out.([]RepoFreshness), nil
}

func asTime(v any) time.Time {
	if t, ok := v.(time.Time); ok {
		return t
	}
	return time.Time{}
}
