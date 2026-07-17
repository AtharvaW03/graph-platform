package index

import (
	"context"
	"errors"
	"io"
	"log"
	"testing"
	"time"
)

// countingFetch returns the given repos and counts invocations; set err to
// make every call fail, or failFrom to start failing at the Nth call
// (1-indexed).
type countingFetch struct {
	repos    []DiscoveredRepo
	err      error
	failFrom int
	calls    int
}

func (f *countingFetch) fetch(context.Context) ([]DiscoveredRepo, error) {
	f.calls++
	if f.err != nil || (f.failFrom > 0 && f.calls >= f.failFrom) {
		if f.err != nil {
			return nil, f.err
		}
		return nil, errors.New("github api down")
	}
	return f.repos, nil
}

func discardLog() *log.Logger { return log.New(io.Discard, "", 0) }

func TestDiscoveryJobSource_FiltersAndSorts(t *testing.T) {
	f := &countingFetch{repos: []DiscoveredRepo{
		{Name: "zeta-service", URL: "https://github.com/org/zeta-service.git", Branch: "main"},
		{Name: "archived-svc", URL: "https://github.com/org/archived-svc.git", Branch: "main", Archived: true},
		{Name: "../evil", URL: "https://github.com/org/evil.git", Branch: "main"},
		{Name: "no-branch", URL: "https://github.com/org/no-branch.git", Branch: ""},
		{Name: "bad-branch", URL: "https://github.com/org/bad-branch.git", Branch: "-rf"},
		{Name: "alpha-service", URL: "https://github.com/org/alpha-service.git", Branch: "develop"},
	}}
	s, err := NewDiscoveryJobSource(f.fetch, nil, time.Hour, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Repositories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "alpha-service" || got[1].Name != "zeta-service" {
		t.Fatalf("got %v, want [alpha-service zeta-service] (archived/invalid filtered, sorted)", got)
	}
	if got[0].Branch != "develop" {
		t.Fatalf("default branch not carried through: %+v", got[0])
	}
}

func TestDiscoveryJobSource_StaticEntriesWinByName(t *testing.T) {
	f := &countingFetch{repos: []DiscoveredRepo{
		{Name: "svc-a", URL: "https://github.com/org/svc-a.git", Branch: "main"},
	}}
	static := []Repository{
		{Name: "svc-a", URL: "https://github.com/org/svc-a.git", Branch: "release-2.x"}, // pin non-default branch
		{Name: "local-only", URL: "file:///C:/repos/local-only", Branch: "main"},        // invisible to discovery
	}
	s, err := NewDiscoveryJobSource(f.fetch, static, time.Hour, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Repositories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want svc-a + local-only", got)
	}
	if got[1].Name != "svc-a" || got[1].Branch != "release-2.x" {
		t.Fatalf("static entry should win by name (branch pin): %+v", got[1])
	}
	if got[0].Name != "local-only" {
		t.Fatalf("static-only entry missing: %v", got)
	}
}

func TestDiscoveryJobSource_TTLCachesFetches(t *testing.T) {
	f := &countingFetch{repos: []DiscoveredRepo{{Name: "svc", URL: "https://x/svc.git", Branch: "main"}}}
	s, err := NewDiscoveryJobSource(f.fetch, nil, time.Hour, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.now = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		if _, err := s.Repositories(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls != 1 {
		t.Fatalf("fetch called %d times within TTL, want 1", f.calls)
	}
	now = now.Add(2 * time.Hour) // TTL expired
	if _, err := s.Repositories(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.calls != 2 {
		t.Fatalf("fetch called %d times after TTL expiry, want 2", f.calls)
	}
}

func TestDiscoveryJobSource_ServesLastKnownGoodOnFailure(t *testing.T) {
	f := &countingFetch{
		repos:    []DiscoveredRepo{{Name: "svc", URL: "https://x/svc.git", Branch: "main"}},
		failFrom: 2,
	}
	s, err := NewDiscoveryJobSource(f.fetch, nil, time.Hour, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.now = func() time.Time { return now }

	if _, err := s.Repositories(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	got, err := s.Repositories(context.Background()) // fetch fails now
	if err != nil {
		t.Fatalf("expected last-known-good fallback, got error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "svc" {
		t.Fatalf("fallback served %v, want the cached manifest", got)
	}
}

func TestDiscoveryJobSource_FirstFetchFailureIsAnError(t *testing.T) {
	f := &countingFetch{err: errors.New("github api down")}
	s, err := NewDiscoveryJobSource(f.fetch, nil, time.Hour, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Repositories(context.Background()); err == nil {
		t.Fatal("a failure before any successful listing must be an error, not an empty manifest")
	}
}
