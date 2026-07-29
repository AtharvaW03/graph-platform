package index

import (
	"context"
	"errors"
	"testing"
)

// fakePauseGate reports paused after the first N checks, so a test can
// assert exactly where in the loop the pause takes effect.
type fakePauseGate struct {
	pauseAfter int // number of checks to allow before reporting paused
	checks     int
	err        error
}

func (g *fakePauseGate) Paused(context.Context) (bool, string, error) {
	g.checks++
	if g.err != nil {
		return false, "", g.err
	}
	return g.checks > g.pauseAfter, "paused by test", nil
}

func TestRunOnce_PauseStopsAtRepoBoundary(t *testing.T) {
	o := testOrchestrator(threeRepos(), nil)
	// Allow one repo, then pause: the loop must stop with exactly one
	// result, never a partially processed second repo.
	o.PauseGate = &fakePauseGate{pauseAfter: 1}

	summary, err := o.RunOnce(context.Background(), Options{})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !summary.Paused {
		t.Error("summary.Paused not set after a pause stopped the cycle")
	}
	if len(summary.Results) != 1 {
		t.Errorf("processed %d repos, want 1 (pause must take effect at the next boundary)", len(summary.Results))
	}
	if len(summary.Results) > 0 && summary.Results[0].Name != "repo-a" {
		t.Errorf("first repo = %q, want repo-a", summary.Results[0].Name)
	}
}

func TestRunOnce_PausedBeforeFirstRepoProcessesNothing(t *testing.T) {
	o := testOrchestrator(threeRepos(), nil)
	o.PauseGate = &fakePauseGate{pauseAfter: 0}

	summary, err := o.RunOnce(context.Background(), Options{})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !summary.Paused {
		t.Error("summary.Paused not set")
	}
	if len(summary.Results) != 0 {
		t.Errorf("processed %d repos while paused, want 0", len(summary.Results))
	}
}

// A broken control channel must not be able to freeze indexing silently -
// that failure mode looks identical to a healthy but stale platform.
func TestRunOnce_PauseGateErrorContinuesIndexing(t *testing.T) {
	o := testOrchestrator(threeRepos(), nil)
	o.PauseGate = &fakePauseGate{err: errors.New("neo4j unreachable")}

	summary, err := o.RunOnce(context.Background(), Options{})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if summary.Paused {
		t.Error("gate error was treated as a pause")
	}
	if len(summary.Results) != 3 {
		t.Errorf("processed %d repos, want all 3 despite the gate error", len(summary.Results))
	}
}

func TestRunOnce_NoPauseGateProcessesEverything(t *testing.T) {
	o := testOrchestrator(threeRepos(), nil)

	summary, err := o.RunOnce(context.Background(), Options{})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if summary.Paused || len(summary.Results) != 3 {
		t.Errorf("without a gate: paused=%v results=%d, want false/3", summary.Paused, len(summary.Results))
	}
}
