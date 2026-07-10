package index

import (
	"context"
	"errors"
	"io"
	"log"
	"testing"

	"graph-platform/internal/importer"
)

// Minimal fakes for the Orchestrator's collaborators - just enough to drive
// RunOnce through a multi-repo loop without touching git, graphify, or Neo4j.

type fakeSource struct{ repos []Repository }

func (f fakeSource) Repositories(context.Context) ([]Repository, error) { return f.repos, nil }

type fakeSyncer struct{}

func (fakeSyncer) Sync(context.Context, Repository, string) (string, error) { return "deadbeef", nil }

type fakeGraphifier struct{}

func (fakeGraphifier) Generate(context.Context, string) (string, error) { return "graph.json", nil }

type fakeImportRunner struct{}

func (fakeImportRunner) Run(context.Context, string, string, string, bool) (*importer.Summary, error) {
	return &importer.Summary{}, nil
}

type fakeStore struct{ m map[string]RepoState }

func newFakeStore() *fakeStore { return &fakeStore{m: map[string]RepoState{}} }

func (s *fakeStore) Get(name string) (RepoState, bool) {
	st, ok := s.m[name]
	return st, ok
}
func (s *fakeStore) Set(state RepoState) error { s.m[state.Name] = state; return nil }
func (s *fakeStore) All() map[string]RepoState { return s.m }

// fakeLeaseRenewer counts calls and fails starting at the failAt'th call
// (1-indexed); failAt == 0 means never fail.
type fakeLeaseRenewer struct {
	calls  int
	failAt int
}

func (f *fakeLeaseRenewer) Renew(context.Context) error {
	f.calls++
	if f.failAt > 0 && f.calls >= f.failAt {
		return errors.New("lease stolen by another owner")
	}
	return nil
}

func testOrchestrator(repos []Repository, lease LeaseRenewer) *Orchestrator {
	return &Orchestrator{
		Source:   fakeSource{repos: repos},
		Syncer:   fakeSyncer{},
		Graphify: fakeGraphifier{},
		Importer: fakeImportRunner{},
		Store:    newFakeStore(),
		WorkDir:  ".",
		Log:      log.New(io.Discard, "", 0),
		Lease:    lease,
	}
}

func threeRepos() []Repository {
	return []Repository{
		{Name: "repo-a", URL: "file:///a", Branch: "main"},
		{Name: "repo-b", URL: "file:///b", Branch: "main"},
		{Name: "repo-c", URL: "file:///c", Branch: "main"},
	}
}

func TestRunOnce_RenewsLeaseOncePerRepo(t *testing.T) {
	lease := &fakeLeaseRenewer{}
	o := testOrchestrator(threeRepos(), lease)

	summary, err := o.RunOnce(context.Background(), Options{})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(summary.Results) != 3 {
		t.Fatalf("got %d results, want 3", len(summary.Results))
	}
	for _, r := range summary.Results {
		if r.Status != StatusSuccess {
			t.Errorf("repo %s: status = %s, want success", r.Name, r.Status)
		}
	}
	if lease.calls != 3 {
		t.Errorf("lease renewed %d times, want 3 (once per repo)", lease.calls)
	}
}

func TestRunOnce_LeaseFailureStopsBeforeNextRepo(t *testing.T) {
	// Fail on the 2nd renewal - repo-a's renewal (call 1) succeeds and it
	// indexes; repo-b's renewal (call 2) fails, so repo-b and repo-c never run.
	lease := &fakeLeaseRenewer{failAt: 2}
	o := testOrchestrator(threeRepos(), lease)

	summary, err := o.RunOnce(context.Background(), Options{})
	if err == nil {
		t.Fatal("expected RunOnce to return an error when lease renewal fails")
	}
	if !errors.Is(err, errLeaseLost) {
		t.Errorf("expected errLeaseLost in the error chain, got: %v", err)
	}
	if len(summary.Results) != 1 {
		t.Fatalf("got %d results, want 1 (only repo-a should have run): %+v", len(summary.Results), summary.Results)
	}
	if summary.Results[0].Name != "repo-a" {
		t.Errorf("only completed repo = %q, want repo-a", summary.Results[0].Name)
	}
	if lease.calls != 2 {
		t.Errorf("lease renewal called %d times, want 2 (stops at the failing call, never tries repo-c)", lease.calls)
	}
}

func TestRunOnce_NoLeaseConfigured_NeverCallsRenew(t *testing.T) {
	o := testOrchestrator(threeRepos(), nil)
	summary, err := o.RunOnce(context.Background(), Options{})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(summary.Results) != 3 {
		t.Fatalf("got %d results, want 3", len(summary.Results))
	}
}
