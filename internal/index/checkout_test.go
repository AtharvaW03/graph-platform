package index

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"a1-knowledge-graph/internal/importer"
)

// headSyncer is a Syncer that also answers remote-head queries, so the
// clone-free skip path can be exercised without a real git remote.
type headSyncer struct {
	remoteHead  string
	remoteErr   error
	headCalls   int
	syncCalls   int
	syncedPaths []string
}

func (s *headSyncer) Sync(_ context.Context, _ Repository, dest string) (string, error) {
	s.syncCalls++
	s.syncedPaths = append(s.syncedPaths, dest)
	// Materialize a checkout the way a real clone would, including a nested
	// extractor cache, so deletion and cache retention are observable.
	if err := os.MkdirAll(filepath.Join(dest, graphifyOutDir, cacheSubdir), 0o755); err != nil {
		return "", err
	}
	for _, f := range []string{"main.go", ".git/config"} {
		p := filepath.Join(dest, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte("source content"), 0o644); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(dest, graphifyOutDir, cacheSubdir, "ast.json"), []byte(`{"nodes":[]}`), 0o644); err != nil {
		return "", err
	}
	// An intermediate artifact that must NOT survive the run.
	if err := os.WriteFile(filepath.Join(dest, graphifyOutDir, "graph.json"), []byte(`{}`), 0o644); err != nil {
		return "", err
	}
	return s.remoteHead, nil
}

func (s *headSyncer) RemoteHead(context.Context, Repository) (string, error) {
	s.headCalls++
	if s.remoteErr != nil {
		return "", s.remoteErr
	}
	return s.remoteHead, nil
}

func checkoutOrchestrator(t *testing.T, sync Syncer) *Orchestrator {
	t.Helper()
	o := testOrchestrator([]Repository{{Name: "repo-a", URL: "file:///a", Branch: "main"}}, nil)
	o.Syncer = sync
	o.WorkDir = t.TempDir()
	o.Log = log.New(io.Discard, "", 0)
	return o
}

func TestCheckout_DeletedAfterSuccessfulRun(t *testing.T) {
	s := &headSyncer{remoteHead: "abc123"}
	o := checkoutOrchestrator(t, s)

	res := o.IndexOne(context.Background(), Repository{Name: "repo-a", URL: "file:///a", Branch: "main"}, false)
	if res.Status != StatusSuccess {
		t.Fatalf("status = %v (%s)", res.Status, res.Error)
	}

	repoPath := filepath.Join(o.WorkDir, "repos", "repo-a")
	if _, err := os.Stat(repoPath); !os.IsNotExist(err) {
		t.Errorf("working copy still present at %s after indexing - source must not outlive the run", repoPath)
	}
}

func TestCheckout_CacheSurvivesButArtifactsDoNot(t *testing.T) {
	s := &headSyncer{remoteHead: "abc123"}
	o := checkoutOrchestrator(t, s)
	o.IndexOne(context.Background(), Repository{Name: "repo-a", URL: "file:///a", Branch: "main"}, false)

	cached := filepath.Join(o.WorkDir, "cache", "repo-a", graphifyOutDir, cacheSubdir, "ast.json")
	if _, err := os.Stat(cached); err != nil {
		t.Errorf("extractor cache not retained (%v) - re-extraction would be a full pass every run", err)
	}
	artifact := filepath.Join(o.WorkDir, "cache", "repo-a", graphifyOutDir, "graph.json")
	if _, err := os.Stat(artifact); err == nil {
		t.Error("intermediate graph.json was retained; only the structural cache should survive")
	}
}

func TestCheckout_DeletedAfterFailedRun(t *testing.T) {
	s := &headSyncer{remoteHead: "abc123"}
	o := checkoutOrchestrator(t, s)
	// Fail at the import stage, after a checkout exists.
	o.Importer = &configurableImportRunner{err: errors.New("import exploded")}

	res := o.IndexOne(context.Background(), Repository{Name: "repo-a", URL: "file:///a", Branch: "main"}, false)
	if res.Status != StatusFailed {
		t.Fatalf("expected failure, got %v", res.Status)
	}
	repoPath := filepath.Join(o.WorkDir, "repos", "repo-a")
	if _, err := os.Stat(repoPath); !os.IsNotExist(err) {
		t.Error("working copy survived a failed run - the easiest way for source to accumulate unnoticed")
	}
}

// panicImportRunner panics instead of erroring, to prove the working copy
// is released even when a stage panics rather than returning an error -
// the same "no source retained on any pipeline exit" property, exercised
// through the path that skips runPipeline's normal early-return logic
// entirely and unwinds via defer instead.
type panicImportRunner struct{}

func (panicImportRunner) Run(context.Context, string, string, string, bool) (*importer.Summary, error) {
	panic("import stage panicked")
}

func TestCheckout_DeletedWhenPipelinePanics(t *testing.T) {
	s := &headSyncer{remoteHead: "abc123"}
	o := checkoutOrchestrator(t, s)
	o.Importer = panicImportRunner{}

	res := o.IndexOne(context.Background(), Repository{Name: "repo-a", URL: "file:///a", Branch: "main"}, false)
	if res.Status != StatusFailed || res.Stage != StagePanic {
		t.Fatalf("status=%v stage=%v, want Failed/StagePanic", res.Status, res.Stage)
	}
	repoPath := filepath.Join(o.WorkDir, "repos", "repo-a")
	if _, err := os.Stat(repoPath); !os.IsNotExist(err) {
		t.Error("working copy survived a panic mid-pipeline - releaseCheckout's defer must fire on every unwind, not just normal returns")
	}
}

func TestCheckout_KeepCheckoutRetainsSource(t *testing.T) {
	s := &headSyncer{remoteHead: "abc123"}
	o := checkoutOrchestrator(t, s)
	o.KeepCheckout = true

	o.IndexOne(context.Background(), Repository{Name: "repo-a", URL: "file:///a", Branch: "main"}, false)

	repoPath := filepath.Join(o.WorkDir, "repos", "repo-a")
	if _, err := os.Stat(filepath.Join(repoPath, "main.go")); err != nil {
		t.Errorf("KeepCheckout did not retain the working copy: %v", err)
	}
}

func TestRemoteHead_UnchangedRepoIsNeverCloned(t *testing.T) {
	s := &headSyncer{remoteHead: "abc123"}
	o := checkoutOrchestrator(t, s)
	repo := Repository{Name: "repo-a", URL: "file:///a", Branch: "main"}

	// First pass indexes and records the commit.
	if res := o.IndexOne(context.Background(), repo, false); res.Status != StatusSuccess {
		t.Fatalf("first pass: %v (%s)", res.Status, res.Error)
	}
	syncsAfterFirst := s.syncCalls

	// Second pass: the remote head is unchanged, so nothing should clone.
	res := o.IndexOne(context.Background(), repo, false)
	if res.Status != StatusSkipped {
		t.Fatalf("second pass status = %v, want skipped", res.Status)
	}
	if s.syncCalls != syncsAfterFirst {
		t.Errorf("repository was cloned despite an unchanged remote head (%d -> %d syncs)", syncsAfterFirst, s.syncCalls)
	}
	if res.Commit != "abc123" {
		t.Errorf("skip result commit = %q, want the remote head", res.Commit)
	}
}

func TestRemoteHead_ChangedRepoIsCloned(t *testing.T) {
	s := &headSyncer{remoteHead: "abc123"}
	o := checkoutOrchestrator(t, s)
	repo := Repository{Name: "repo-a", URL: "file:///a", Branch: "main"}
	o.IndexOne(context.Background(), repo, false)

	s.remoteHead = "def456" // someone pushed
	before := s.syncCalls
	res := o.IndexOne(context.Background(), repo, false)

	if res.Status != StatusSuccess {
		t.Fatalf("changed repo: %v (%s)", res.Status, res.Error)
	}
	if s.syncCalls != before+1 {
		t.Error("a changed remote head did not trigger a clone")
	}
}

// An unreachable remote must never be read as "unchanged" - that would
// freeze a repository at an old commit with no error anywhere.
func TestRemoteHead_ErrorFallsBackToFullSync(t *testing.T) {
	s := &headSyncer{remoteHead: "abc123"}
	o := checkoutOrchestrator(t, s)
	repo := Repository{Name: "repo-a", URL: "file:///a", Branch: "main"}
	o.IndexOne(context.Background(), repo, false)

	s.remoteErr = errors.New("network unreachable")
	before := s.syncCalls
	res := o.IndexOne(context.Background(), repo, false)

	if s.syncCalls != before+1 {
		t.Error("remote head failure did not fall back to a full sync")
	}
	// The full sync then finds the same commit and skips - correct, but it
	// got there by checking, not by assuming.
	if res.Status != StatusSkipped {
		t.Errorf("status = %v, want skipped via the post-sync check", res.Status)
	}
}

func TestRemoteHead_ForceAlwaysClones(t *testing.T) {
	s := &headSyncer{remoteHead: "abc123"}
	o := checkoutOrchestrator(t, s)
	repo := Repository{Name: "repo-a", URL: "file:///a", Branch: "main"}
	o.IndexOne(context.Background(), repo, false)

	before := s.syncCalls
	if res := o.IndexOne(context.Background(), repo, true); res.Status != StatusSuccess {
		t.Fatalf("forced run: %v", res.Status)
	}
	if s.syncCalls != before+1 {
		t.Error("--force did not re-clone an unchanged repository")
	}
}

// A Syncer with no RemoteHead support must still work, just without the
// clone-free skip.
func TestRemoteHead_UnsupportedSyncerStillIndexes(t *testing.T) {
	o := checkoutOrchestrator(t, fakeSyncer{})
	res := o.IndexOne(context.Background(), Repository{Name: "repo-a", URL: "file:///a", Branch: "main"}, false)
	if res.Status != StatusSuccess {
		t.Fatalf("status = %v (%s)", res.Status, res.Error)
	}
}
