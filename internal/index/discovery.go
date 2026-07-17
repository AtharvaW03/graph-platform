package index

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

// DiscoveredRepo is one candidate repository from a discovery backend
// (e.g. the GitHub App installation listing), before policy filtering.
type DiscoveredRepo struct {
	Name     string
	URL      string
	Branch   string
	Archived bool
}

// DiscoveryJobSource is a JobSource whose manifest comes from a remote
// discovery call instead of a static config list: the set of repos the
// GitHub App is installed on IS the manifest, so installing/uninstalling the
// App on a repo adds/removes it from the graph without touching any config.
//
// Policy applied to what Fetch returns:
//   - archived repos are dropped (nothing new to index, and retirement will
//     eventually clean their graph data once dropped here);
//   - names failing the indexer's repo-name validation are skipped loudly
//     (same rule as config validation, keeping filesystem/graph keys safe);
//   - Static entries win by name over discovered ones, so an operator can
//     pin a non-default branch (or keep a repo the discovery source can't
//     see) by leaving it in the YAML.
//
// Results are cached for TTL so webhook-triggered cycles don't hammer the
// GitHub API, and the last successful listing is served if a refresh fails -
// a GitHub outage must not shrink the manifest, because a shrunken manifest
// starts retirement countdowns (the mass-retirement guard is the second net
// behind this one). Safe for concurrent use.
type DiscoveryJobSource struct {
	fetch  func(ctx context.Context) ([]DiscoveredRepo, error)
	static []Repository
	ttl    time.Duration
	log    *log.Logger
	now    func() time.Time

	mu        sync.Mutex
	cached    []Repository
	fetchedAt time.Time
}

func NewDiscoveryJobSource(fetch func(ctx context.Context) ([]DiscoveredRepo, error), static []Repository, ttl time.Duration, logger *log.Logger) (*DiscoveryJobSource, error) {
	if fetch == nil {
		return nil, fmt.Errorf("discovery fetch function is required")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("discovery ttl must be > 0")
	}
	return &DiscoveryJobSource{
		fetch:  fetch,
		static: append([]Repository(nil), static...),
		ttl:    ttl,
		log:    logger,
		now:    time.Now,
	}, nil
}

// Repositories serves the merged manifest, refreshing from the discovery
// backend when the cache is older than TTL. A refresh failure after at least
// one success degrades to the cached list with a warning; a failure before
// any success is an error (an empty first manifest is indistinguishable from
// a broken one, and returning it would look like a mass retirement).
func (s *DiscoveryJobSource) Repositories(ctx context.Context) ([]Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cached != nil && s.now().Sub(s.fetchedAt) < s.ttl {
		return append([]Repository(nil), s.cached...), nil
	}

	discovered, err := s.fetch(ctx)
	if err != nil {
		if s.cached != nil {
			s.log.Printf("WARNING: repository discovery failed, serving last-known list of %d repos (age %s): %v",
				len(s.cached), s.now().Sub(s.fetchedAt).Round(time.Second), err)
			return append([]Repository(nil), s.cached...), nil
		}
		return nil, fmt.Errorf("repository discovery: %w", err)
	}

	s.cached = s.merge(discovered)
	s.fetchedAt = s.now()
	return append([]Repository(nil), s.cached...), nil
}

// merge applies filtering policy to the discovered list and overlays the
// static entries (static wins by name). Output is sorted by name so cycles
// process repos in a stable order.
func (s *DiscoveryJobSource) merge(discovered []DiscoveredRepo) []Repository {
	byName := make(map[string]Repository, len(discovered)+len(s.static))
	for _, d := range discovered {
		switch {
		case d.Archived:
			continue
		case !validRepoName.MatchString(d.Name):
			s.log.Printf("WARNING: discovery skipped repo %q: name fails validation (%s)", d.Name, validRepoName.String())
			continue
		case d.URL == "" || d.Branch == "":
			s.log.Printf("WARNING: discovery skipped repo %q: missing clone URL or default branch", d.Name)
			continue
		case !validBranch.MatchString(d.Branch):
			s.log.Printf("WARNING: discovery skipped repo %q: default branch %q fails validation", d.Name, d.Branch)
			continue
		}
		byName[d.Name] = Repository{Name: d.Name, URL: d.URL, Branch: d.Branch}
	}
	for _, r := range s.static {
		byName[r.Name] = r
	}
	out := make([]Repository, 0, len(byName))
	for _, r := range byName {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
