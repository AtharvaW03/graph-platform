package index

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"testing"
	"time"

	"a1-knowledge-graph/internal/extract"
	"a1-knowledge-graph/internal/importer"
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

// fakeStamper records StampRepoSync calls as "name:indexed" strings.
type fakeStamper struct{ calls []string }

func (f *fakeStamper) StampRepoSync(_ context.Context, repo string, indexed bool) error {
	f.calls = append(f.calls, fmt.Sprintf("%s:%v", repo, indexed))
	return nil
}

// repoFailImporter fails the import stage for one named repo.
type repoFailImporter struct{ fail string }

func (r repoFailImporter) Run(_ context.Context, repo, _, _ string, _ bool) (*importer.Summary, error) {
	if repo == r.fail {
		return nil, errors.New("import failed")
	}
	return &importer.Summary{}, nil
}

// TestRunOnce_StampsFreshness: a successful repo stamps indexed=true, an
// unchanged-HEAD skip stamps indexed=false, and a failed repo is not
// stamped.
func TestRunOnce_StampsFreshness(t *testing.T) {
	orch := testOrchestrator(threeRepos(), nil)
	st := &fakeStamper{}
	orch.SyncStamper = st
	orch.Importer = repoFailImporter{fail: "repo-c"}
	if err := orch.Store.Set(RepoState{
		Name:              "repo-b",
		LastStatus:        StatusSuccess,
		LastIndexedCommit: "deadbeef",
		SchemaVersion:     GraphSchemaVersion,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := orch.RunOnce(context.Background(), Options{}); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	want := []string{"repo-a:true", "repo-b:false"}
	if len(st.calls) != len(want) || st.calls[0] != want[0] || st.calls[1] != want[1] {
		t.Fatalf("stamps = %v, want %v", st.calls, want)
	}
}

// fakeExtractor always fails - used to drive the extractor fail-closed gate.
type fakeExtractor struct {
	name string
	err  error
}

func (f fakeExtractor) Name() string { return f.name }
func (f fakeExtractor) Extract(context.Context, string, string) (*extract.Fragment, error) {
	return nil, f.err
}

// configurableImportRunner lets a test control what the import stage returns
// (including a mismatch or sweep residue) and counts calls, so a test can
// assert the importer was never reached (the fail-closed extractor gate).
type configurableImportRunner struct {
	summary *importer.Summary
	err     error
	calls   int
}

func (r *configurableImportRunner) Run(context.Context, string, string, string, bool) (*importer.Summary, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	if r.summary != nil {
		return r.summary, nil
	}
	return &importer.Summary{}, nil
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

func oneRepo() Repository {
	return Repository{Name: "repo-x", URL: "file:///x", Branch: "main"}
}

func TestIndexOne_ExtractorErrorFailsClosedByDefault(t *testing.T) {
	importRunner := &configurableImportRunner{}
	store := newFakeStore()
	o := &Orchestrator{
		Source:   fakeSource{},
		Syncer:   fakeSyncer{},
		Graphify: fakeGraphifier{},
		Importer: importRunner,
		Store:    store,
		WorkDir:  ".",
		Log:      log.New(io.Discard, "", 0),
		Extractors: &extract.Runner{
			Extractors: []extract.Extractor{fakeExtractor{name: "deps", err: errors.New("manifest parse failed")}},
			Log:        log.New(io.Discard, "", 0),
		},
		// AllowPartialExtractorErrors left false: default fail-closed.
	}

	result := o.IndexOne(context.Background(), oneRepo(), false)

	if result.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", result.Status)
	}
	if result.Stage != StageExtract {
		t.Errorf("stage = %s, want %s", result.Stage, StageExtract)
	}
	if result.ExtractorErrors["deps"] == "" {
		t.Error("ExtractorErrors[\"deps\"] should be recorded even though the run failed closed")
	}
	if importRunner.calls != 0 {
		t.Errorf("importer.Run called %d times, want 0 - fail-closed must never import", importRunner.calls)
	}

	st, ok := store.Get("repo-x")
	if !ok {
		t.Fatal("state should have been persisted (failure, not cancellation)")
	}
	if st.LastIndexedCommit != "" {
		t.Errorf("LastIndexedCommit = %q, want empty - state must not advance on a failed-closed run", st.LastIndexedCommit)
	}
	if st.ConsecutiveFails != 1 {
		t.Errorf("ConsecutiveFails = %d, want 1", st.ConsecutiveFails)
	}
}

func TestIndexOne_ExtractorErrorAllowPartialImportsAnyway(t *testing.T) {
	importRunner := &configurableImportRunner{}
	store := newFakeStore()
	o := &Orchestrator{
		Source:   fakeSource{},
		Syncer:   fakeSyncer{},
		Graphify: fakeGraphifier{},
		Importer: importRunner,
		Store:    store,
		WorkDir:  ".",
		Log:      log.New(io.Discard, "", 0),
		Extractors: &extract.Runner{
			Extractors: []extract.Extractor{fakeExtractor{name: "deps", err: errors.New("manifest parse failed")}},
			Log:        log.New(io.Discard, "", 0),
		},
		AllowPartialExtractorErrors: true,
	}

	result := o.IndexOne(context.Background(), oneRepo(), false)

	if result.Status != StatusSuccess {
		t.Fatalf("status = %s, want success (allow_partial preserves the old behavior)", result.Status)
	}
	if result.ExtractorErrors["deps"] == "" {
		t.Error("ExtractorErrors[\"deps\"] should still be recorded")
	}
	if importRunner.calls != 1 {
		t.Errorf("importer.Run called %d times, want 1 - allow_partial must still import", importRunner.calls)
	}

	st, ok := store.Get("repo-x")
	if !ok || st.LastIndexedCommit != "deadbeef" {
		t.Errorf("state should have advanced to commit deadbeef, got %+v (ok=%v)", st, ok)
	}
}

func TestIndexOne_MismatchFailsRepoAndDoesNotAdvanceState(t *testing.T) {
	importRunner := &configurableImportRunner{summary: &importer.Summary{
		Commit:       "deadbeef",
		NodesTotal:   10,
		NodesInGraph: 7, // mismatch
	}}
	store := newFakeStore()
	o := &Orchestrator{
		Source:   fakeSource{},
		Syncer:   fakeSyncer{},
		Graphify: fakeGraphifier{},
		Importer: importRunner,
		Store:    store,
		WorkDir:  ".",
		Log:      log.New(io.Discard, "", 0),
	}

	result := o.IndexOne(context.Background(), oneRepo(), false)

	if result.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", result.Status)
	}
	if result.Stage != StageImport {
		t.Errorf("stage = %s, want %s", result.Stage, StageImport)
	}
	if !result.Mismatch {
		t.Error("Mismatch should be true")
	}
	if result.Error == "" {
		t.Error("Error should describe the mismatch")
	}

	st, ok := store.Get("repo-x")
	if !ok {
		t.Fatal("state should have been persisted (failure, not cancellation)")
	}
	if st.LastIndexedCommit != "" {
		t.Errorf("LastIndexedCommit = %q, want empty - a mismatch must not advance state", st.LastIndexedCommit)
	}
}

func TestIndexOne_SweepResidueFailsRepoAndDoesNotAdvanceState(t *testing.T) {
	importRunner := &configurableImportRunner{summary: &importer.Summary{
		Commit:            "deadbeef",
		NodesTotal:        10,
		NodesInGraph:      10, // no mismatch
		SweepResidueNodes: 2,
		SweepResidueRels:  1,
	}}
	store := newFakeStore()
	o := &Orchestrator{
		Source:   fakeSource{},
		Syncer:   fakeSyncer{},
		Graphify: fakeGraphifier{},
		Importer: importRunner,
		Store:    store,
		WorkDir:  ".",
		Log:      log.New(io.Discard, "", 0),
	}

	result := o.IndexOne(context.Background(), oneRepo(), false)

	if result.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", result.Status)
	}
	if result.Stage != StageImport {
		t.Errorf("stage = %s, want %s", result.Stage, StageImport)
	}
	if !result.SweepResidue {
		t.Error("SweepResidue should be true")
	}
	if result.Mismatch {
		t.Error("Mismatch should be false - only sweep residue is set in this case")
	}

	st, ok := store.Get("repo-x")
	if !ok {
		t.Fatal("state should have been persisted (failure, not cancellation)")
	}
	if st.LastIndexedCommit != "" {
		t.Errorf("LastIndexedCommit = %q, want empty - sweep residue must not advance state", st.LastIndexedCommit)
	}
}

// TestIndexOne_SchemaVersionMismatchForcesReindex: an unchanged HEAD is only
// skippable when the recorded schema version matches - a version bump must
// roll the migration out without --force.
func TestIndexOne_SchemaVersionMismatchForcesReindex(t *testing.T) {
	imp := &configurableImportRunner{}
	o := testOrchestrator(threeRepos()[:1], nil)
	o.Importer = imp
	o.Store.Set(RepoState{
		Name:              "repo-a",
		LastStatus:        StatusSuccess,
		LastIndexedCommit: "deadbeef", // matches fakeSyncer
		SchemaVersion:     GraphSchemaVersion - 1,
	})

	res := o.IndexOne(context.Background(), threeRepos()[0], false)
	if res.Status == StatusSkipped {
		t.Fatalf("repo was skipped despite stale schema version")
	}
	if imp.calls != 1 {
		t.Fatalf("importer calls = %d, want 1 (schema bump must re-import)", imp.calls)
	}
	st, _ := o.Store.Get("repo-a")
	if st.SchemaVersion != GraphSchemaVersion {
		t.Fatalf("persisted SchemaVersion = %d, want %d", st.SchemaVersion, GraphSchemaVersion)
	}
}

// TestIndexOne_MatchingSchemaVersionSkipsUnchangedHead: with commit and
// schema both current, the unchanged-HEAD skip still applies.
func TestIndexOne_MatchingSchemaVersionSkipsUnchangedHead(t *testing.T) {
	imp := &configurableImportRunner{}
	o := testOrchestrator(threeRepos()[:1], nil)
	o.Importer = imp
	o.Store.Set(RepoState{
		Name:              "repo-a",
		LastStatus:        StatusSuccess,
		LastIndexedCommit: "deadbeef",
		SchemaVersion:     GraphSchemaVersion,
	})

	res := o.IndexOne(context.Background(), threeRepos()[0], false)
	if res.Status != StatusSkipped {
		t.Fatalf("status = %s, want skipped (commit and schema both unchanged)", res.Status)
	}
	if imp.calls != 0 {
		t.Fatalf("importer calls = %d, want 0", imp.calls)
	}
}

// fakeRetirer serves a fixed graph-repo list and records deletions.
type fakeRetirer struct {
	names     []string
	listCalls int
	deleted   []string
	delErr    error
}

func (f *fakeRetirer) ListRepositoryNames(context.Context) ([]string, error) {
	f.listCalls++
	return f.names, nil
}

func (f *fakeRetirer) DeleteRepositoryGraph(_ context.Context, repo string) (int, int, error) {
	if f.delErr != nil {
		return 0, 0, f.delErr
	}
	f.deleted = append(f.deleted, repo)
	return 7, 9, nil
}

// TestReconcileRetired_WarnsThenReapsAfterGrace is the ghost-repo scenario: a
// (:Repository) in the graph that's no longer in the config gets a warning
// first, survives runs inside the grace window, and is deleted only after it.
func TestReconcileRetired_WarnsThenReapsAfterGrace(t *testing.T) {
	config := threeRepos()[:1] // repo-a stays configured
	fr := &fakeRetirer{names: []string{"repo-a", "ghost"}}
	o := testOrchestrator(config, nil)
	o.Retirer = fr

	t0 := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	o.Clock = func() time.Time { return t0 }

	o.reconcileRetired(context.Background(), config)
	if len(fr.deleted) != 0 {
		t.Fatalf("first run deleted %v, want warning only", fr.deleted)
	}
	st, ok := o.Store.Get("ghost")
	if !ok || !st.RetirementWarnedAt.Equal(t0) {
		t.Fatalf("ghost state = %+v (ok=%v), want RetirementWarnedAt=%s", st, ok, t0)
	}

	// Inside the grace window: still nothing deleted.
	o.Clock = func() time.Time { return t0.Add(retirementGrace / 2) }
	o.reconcileRetired(context.Background(), config)
	if len(fr.deleted) != 0 {
		t.Fatalf("in-grace run deleted %v, want none", fr.deleted)
	}

	// Past the grace window: ghost is reaped, configured repo untouched,
	// state reset so a later re-add can't skip on a stale commit.
	o.Clock = func() time.Time { return t0.Add(retirementGrace + time.Minute) }
	o.reconcileRetired(context.Background(), config)
	if len(fr.deleted) != 1 || fr.deleted[0] != "ghost" {
		t.Fatalf("deleted = %v, want exactly [ghost]", fr.deleted)
	}
	st, _ = o.Store.Get("ghost")
	if !st.RetirementWarnedAt.IsZero() || st.LastIndexedCommit != "" || st.LastStatus != "" {
		t.Fatalf("ghost state after reap = %+v, want zeroed", st)
	}
}

// TestReconcileRetired_BackInConfigCancels: re-adding a warned repo to the
// config clears the pending retirement instead of reaping it later.
func TestReconcileRetired_BackInConfigCancels(t *testing.T) {
	config := threeRepos()[:1]
	fr := &fakeRetirer{names: []string{"repo-a"}}
	o := testOrchestrator(config, nil)
	o.Retirer = fr

	warned := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	o.Store.Set(RepoState{Name: "repo-a", RetirementWarnedAt: warned})
	o.Clock = func() time.Time { return warned.Add(2 * retirementGrace) }

	o.reconcileRetired(context.Background(), config)
	if len(fr.deleted) != 0 {
		t.Fatalf("deleted = %v, want none (repo is configured)", fr.deleted)
	}
	st, _ := o.Store.Get("repo-a")
	if !st.RetirementWarnedAt.IsZero() {
		t.Fatalf("RetirementWarnedAt = %s, want cleared", st.RetirementWarnedAt)
	}
}

// TestReconcileRetired_DeleteFailureRetries: a failed delete keeps the
// warning timestamp so the next run past grace retries instead of forgetting.
func TestReconcileRetired_DeleteFailureRetries(t *testing.T) {
	config := threeRepos()[:1]
	fr := &fakeRetirer{names: []string{"ghost"}, delErr: errors.New("neo4j down")}
	o := testOrchestrator(config, nil)
	o.Retirer = fr

	warned := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	o.Store.Set(RepoState{Name: "ghost", RetirementWarnedAt: warned})
	o.Clock = func() time.Time { return warned.Add(2 * retirementGrace) }

	o.reconcileRetired(context.Background(), config)
	st, _ := o.Store.Get("ghost")
	if !st.RetirementWarnedAt.Equal(warned) {
		t.Fatalf("RetirementWarnedAt = %s, want unchanged %s after failed delete", st.RetirementWarnedAt, warned)
	}

	fr.delErr = nil
	o.reconcileRetired(context.Background(), config)
	if len(fr.deleted) != 1 || fr.deleted[0] != "ghost" {
		t.Fatalf("deleted = %v, want [ghost] on retry", fr.deleted)
	}
}

// TestReconcileRetired_MassRetirementGuard: more than half the graph missing
// from the config means a wrong config file, not a retirement - nothing is
// warned or deleted, even for repos already past their grace period.
func TestReconcileRetired_MassRetirementGuard(t *testing.T) {
	config := threeRepos()[:1] // only repo-a configured
	fr := &fakeRetirer{names: []string{"repo-a", "ghost-1", "ghost-2", "ghost-3"}}
	o := testOrchestrator(config, nil)
	o.Retirer = fr

	// ghost-1 was already warned long ago; the guard must protect it anyway.
	warned := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	o.Store.Set(RepoState{Name: "ghost-1", RetirementWarnedAt: warned})
	o.Clock = func() time.Time { return warned.Add(2 * retirementGrace) }

	o.reconcileRetired(context.Background(), config)
	if len(fr.deleted) != 0 {
		t.Fatalf("guard run deleted %v, want none", fr.deleted)
	}
	if st, ok := o.Store.Get("ghost-2"); ok && !st.RetirementWarnedAt.IsZero() {
		t.Fatalf("guard run warned ghost-2 (%+v), want no new warnings", st)
	}
	st, _ := o.Store.Get("ghost-1")
	if !st.RetirementWarnedAt.Equal(warned) {
		t.Fatalf("ghost-1 RetirementWarnedAt = %s, want unchanged %s", st.RetirementWarnedAt, warned)
	}

	// One missing repo out of the same graph is a normal retirement: the
	// guard must not block it.
	fr2 := &fakeRetirer{names: []string{"repo-a", "repo-b", "repo-c", "ghost-1"}}
	o.Retirer = fr2
	o.reconcileRetired(context.Background(), threeRepos())
	if len(fr2.deleted) != 1 || fr2.deleted[0] != "ghost-1" {
		t.Fatalf("deleted = %v, want [ghost-1] (single retirement past grace)", fr2.deleted)
	}
}

// TestRunOnce_TargetedRunSkipsRetirement: --repo runs are surgical and must
// not reconcile (or delete) anything as a side effect; full runs must.
func TestRunOnce_TargetedRunSkipsRetirement(t *testing.T) {
	fr := &fakeRetirer{names: []string{"ghost"}}
	o := testOrchestrator(threeRepos(), nil)
	o.Retirer = fr

	if _, err := o.RunOnce(context.Background(), Options{Names: []string{"repo-a"}}); err != nil {
		t.Fatal(err)
	}
	if fr.listCalls != 0 {
		t.Fatalf("targeted run hit the retirer %d times, want 0", fr.listCalls)
	}

	if _, err := o.RunOnce(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if fr.listCalls != 1 {
		t.Fatalf("full run hit the retirer %d times, want 1", fr.listCalls)
	}
}
