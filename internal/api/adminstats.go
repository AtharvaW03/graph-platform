package api

import (
	"context"
	"time"

	"a1-knowledge-graph/internal/admin"
	"a1-knowledge-graph/internal/query"
)

// GraphStatsAdapter adapts *query.Service to admin.GraphStats. It lives
// here rather than in internal/admin so the admin package stays free of
// the query domain types.
type GraphStatsAdapter struct {
	svc *query.Service
	// staleAfter mirrors the /freshness contract so the dashboard and the
	// public endpoint agree on what "stale" means.
	staleAfter time.Duration
}

func NewGraphStatsAdapter(svc *query.Service) *GraphStatsAdapter {
	return &GraphStatsAdapter{svc: svc, staleAfter: freshnessStaleAfter}
}

func (a *GraphStatsAdapter) ListRepositories(ctx context.Context) ([]admin.RepoSize, error) {
	repos, err := a.svc.ListRepositories(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]admin.RepoSize, 0, len(repos))
	for _, r := range repos {
		out = append(out, admin.RepoSize{Name: r.Name, Nodes: r.Nodes})
	}
	return out, nil
}

func (a *GraphStatsAdapter) Freshness(ctx context.Context) ([]admin.RepoFreshness, error) {
	rows, err := a.svc.Freshness(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]admin.RepoFreshness, 0, len(rows))
	for _, r := range rows {
		age := int64(now.Sub(r.LastSyncedAt).Seconds())
		if age < 0 {
			age = 0
		}
		out = append(out, admin.RepoFreshness{
			Repo:       r.Repo,
			AgeSeconds: age,
			Stale:      now.Sub(r.LastSyncedAt) > a.staleAfter,
		})
	}
	return out, nil
}
