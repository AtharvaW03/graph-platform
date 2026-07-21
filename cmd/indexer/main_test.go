package main

import (
	"context"
	"errors"
	"io"
	"log"
	"testing"
	"time"

	"a1-knowledge-graph/internal/importer"
	"a1-knowledge-graph/internal/index"
)

func TestExitErr(t *testing.T) {
	hbErr := errors.New("lease lost")

	cases := []struct {
		name    string
		failed  int
		hbErr   error
		wantNil bool
	}{
		{"clean run, no heartbeat error", 0, nil, true},
		{"failed repos, no heartbeat error", 2, nil, false},
		{"heartbeat fatal wins even with zero failed repos", 0, hbErr, false},
		{"heartbeat fatal wins over failed repos too", 3, hbErr, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := exitErr(c.failed, c.hbErr)
			if (got == nil) != c.wantNil {
				t.Errorf("exitErr(%d, %v) = %v, want nil=%v", c.failed, c.hbErr, got, c.wantNil)
			}
			if c.hbErr != nil && got != nil && !errors.Is(got, c.hbErr) {
				t.Errorf("exitErr(%d, %v) = %v, does not wrap the heartbeat error", c.failed, c.hbErr, got)
			}
		})
	}
}

// Minimal fakes reproducing cmd/indexer's real wiring (Orchestrator +
// LeaseHeartbeat sharing runCtx), just enough to drive a one-shot RunOnce
// without touching git, graphify, or Neo4j.

type fakeRepoSource struct{ repos []index.Repository }

func (f fakeRepoSource) Repositories(context.Context) ([]index.Repository, error) {
	return f.repos, nil
}

// blockingSyncer blocks until ctx is canceled, simulating a long-running sync
// (or graphify, or import) stage that's still in flight when the lease is lost.
type blockingSyncer struct{}

func (blockingSyncer) Sync(ctx context.Context, _ index.Repository, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

type noopGraphifier struct{}

func (noopGraphifier) Generate(context.Context, string) (string, error) { return "graph.json", nil }

type noopImportRunner struct{}

func (noopImportRunner) Run(context.Context, string, string, string, bool) (*importer.Summary, error) {
	return &importer.Summary{}, nil
}

type noopStore struct{}

func (noopStore) Get(string) (index.RepoState, bool) { return index.RepoState{}, false }
func (noopStore) Set(index.RepoState) error          { return nil }
func (noopStore) All() map[string]index.RepoState    { return nil }

// TestLeaseLossDuringOneShotRun_ExitsNonZero reproduces the exact bug this
// batch's ITEM 1 fixes: a lease heartbeat that gives up mid-run cancels
// runCtx; Orchestrator.RunOnce sees that as plain context cancellation and
// breaks its loop, returning a NIL error (the repo is recorded canceled, not
// failed). Before this fix, main() only checked RunOnce's own error and the
// failed-repo count - both clean here - so the process would have exited 0
// even though a confirmed lease loss happened mid-run. exitErr must catch it
// via the heartbeat's own recorded error, independent of ctx state.
func TestLeaseLossDuringOneShotRun_ExitsNonZero(t *testing.T) {
	ctx := context.Background()
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	heartbeat := &index.LeaseHeartbeat{
		Renew:    func(context.Context) error { return errors.New("lease stolen by another owner") },
		Interval: 5 * time.Millisecond,
		Log:      log.New(io.Discard, "", 0),
		IsLost:   func(error) bool { return true }, // every renewal failure here is a confirmed loss
		OnFatal:  func(error) { cancelRun() },
	}
	go heartbeat.Run(ctx)

	orch := &index.Orchestrator{
		Source:   fakeRepoSource{repos: []index.Repository{{Name: "slow-repo", URL: "file:///x", Branch: "main"}}},
		Syncer:   blockingSyncer{},
		Graphify: noopGraphifier{},
		Importer: noopImportRunner{},
		Store:    noopStore{},
		WorkDir:  ".",
		Log:      log.New(io.Discard, "", 0),
	}

	summary, err := orch.RunOnce(runCtx, index.Options{})

	// Reproduce the bug's premise first - if this ever starts failing because
	// RunOnce itself changed to return an error here, exitErr's heartbeat
	// check becomes belt-and-braces rather than load-bearing, but must stay.
	if err != nil {
		t.Fatalf("RunOnce returned %v, want nil - this test reproduces the case where it doesn't error", err)
	}
	_, _, _, failed := summary.Counts()
	if failed != 0 {
		t.Fatalf("failed = %d, want 0 (the in-flight repo should be canceled, not failed)", failed)
	}

	if fatal := exitErr(failed, heartbeat.FatalErr()); fatal == nil {
		t.Fatal("exitErr(0, heartbeat.FatalErr()) = nil, want non-nil: a confirmed lease loss must exit nonzero even when RunOnce reports zero failed repos")
	}
}
