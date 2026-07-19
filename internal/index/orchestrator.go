package index

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"graph-platform/internal/extract"
	"graph-platform/internal/importer"
)

// GraphSchemaVersion identifies the shape of what the pipeline writes to
// Neo4j. Bump it whenever a change requires already-indexed repos to be
// re-imported despite an unchanged HEAD: the unchanged-HEAD skip only
// applies when a repo's recorded SchemaVersion matches, so a bump rolls the
// migration out automatically on the next cycle.
const GraphSchemaVersion = 9

// Orchestrator drives the per-repo pipeline for a configured set of
// repositories. Every step is delegated to a pluggable component (Source,
// Syncer, Graphifier, Importer, Store, optional Scheduler and
// HealthChecker), so swapping one collaborator can add parallel indexing or
// a new transport without touching the pipeline itself.
type Orchestrator struct {
	Source   JobSource
	Syncer   Syncer
	Graphify Graphifier
	Importer ImportRunner
	Store    StateStore
	WorkDir  string
	Log      *log.Logger
	Clock    func() time.Time

	// Extractors, if non-nil, runs the configured platform extractors after
	// graphify and merges their fragments into the unified graph.json before
	// the importer reads it. Per-extractor errors are recorded on the
	// RepoResult.
	Extractors *extract.Runner

	// AllowPartialExtractorErrors, when true, logs an extractor error and
	// imports the partial graph anyway. Default false: an extractor error
	// fails the repo and nothing imports.
	AllowPartialExtractorErrors bool

	// HealthChecker, if set, is pinged before each cycle in continuous mode.
	// A failed ping is logged but does not abort - the cycle proceeds and
	// individual stage failures will be recorded per-repo.
	HealthChecker HealthChecker

	// Lease, if set, is renewed before every repo (in both RunOnce and
	// RunForever, which shares RunOnce's loop). Unlike HealthChecker, a
	// failed renewal is fatal to the run - see LeaseRenewer.
	Lease LeaseRenewer

	// Retirer, if set, reconciles the graph against the config at the start
	// of every full run: a (:Repository) no longer configured is warned
	// first, then its graph data deleted on a later run after
	// retirementGrace.
	Retirer RepoRetirer
}

// RepoRetirer lists and deletes per-repository graph data, backing
// config-vs-graph retirement reconciliation. *neo4j.Client implements it.
type RepoRetirer interface {
	ListRepositoryNames(ctx context.Context) ([]string, error)
	DeleteRepositoryGraph(ctx context.Context, repo string) (nodes, rels int, err error)
}

// retirementGrace is the minimum time between the "repo missing from config"
// warning and its graph data being deleted, so a config mistake can be
// corrected before anything is removed.
const retirementGrace = time.Hour

// ImportRunner is the importer-side interface. The default implementation
// adapts internal/importer.Run; tests or alternative sinks can swap in.
type ImportRunner interface {
	Run(ctx context.Context, repo, commit, graphPath string, rewriteAll bool) (*importer.Summary, error)
}

// HealthChecker pings a downstream dependency. The default impl wraps
// *neo4j.Client; future variants can compose multiple checks.
type HealthChecker interface {
	VerifyConnectivity(ctx context.Context) error
}

// LeaseRenewer extends the writer lease. RunOnce calls Renew before every
// repo; a failed renewal stops the run immediately.
type LeaseRenewer interface {
	Renew(ctx context.Context) error
}

// errLeaseLost marks a RunOnce error as a lease renewal failure. RunForever
// stops the daemon on it instead of retrying: a lost lease means another
// writer holds the database.
var errLeaseLost = errors.New("writer lease lost")

// Options modulate a single RunOnce invocation.
type Options struct {
	// Names selects a subset of repositories; empty means "all".
	Names []string
	// Force re-indexes even if HEAD matches the previously-indexed commit.
	Force bool
}

// RunOnce indexes every selected repository sequentially. One repo failing
// never stops the others - failures are recorded on the result and state is
// flushed before moving on. ctx cancellation aborts the current repo and
// stops the loop after. Panics in collaborators are recovered and recorded.
//
// When a Lease is configured it is renewed before every repo, so a long run
// cannot outlive the lease TTL between repos. A renewal failure stops the
// loop immediately and RunOnce returns an error alongside the results
// collected so far.
func (o *Orchestrator) RunOnce(ctx context.Context, opts Options) (summary RunSummary, err error) {
	summary = RunSummary{StartedAt: o.now()}
	defer func() { summary.FinishedAt = o.now() }()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("RunOnce panic: %v", p)
		}
	}()

	repos, err := o.Source.Repositories(ctx)
	if err != nil {
		return summary, fmt.Errorf("load repositories: %w", err)
	}

	// Retirement reconciliation runs only on full runs, never on --repo
	// targeted runs. It writes to Neo4j, so it renews the lease first.
	if o.Retirer != nil && len(opts.Names) == 0 {
		if o.Lease != nil {
			if err := o.Lease.Renew(ctx); err != nil {
				return summary, fmt.Errorf("%w: renewal failed before retirement reconciliation: %w", errLeaseLost, err)
			}
		}
		o.reconcileRetired(ctx, repos)
	}

	repos, err = filterRepos(repos, opts.Names)
	if err != nil {
		return summary, err
	}

	for _, repo := range repos {
		if ctx.Err() != nil {
			o.Log.Printf("context canceled, stopping after %d/%d repositories", len(summary.Results), len(repos))
			break
		}
		if o.Lease != nil {
			if err := o.Lease.Renew(ctx); err != nil {
				o.Log.Printf("writer lease renewal failed before %s, stopping: %v", repo.Name, err)
				return summary, fmt.Errorf("%w: renewal failed before repo %q: %w", errLeaseLost, repo.Name, err)
			}
		}
		result := o.IndexOne(ctx, repo, opts.Force)
		summary.Results = append(summary.Results, result)
	}

	return summary, nil
}

// RunForever loops RunOnce on the configured Scheduler until ctx is
// canceled. Cycles never overlap: the next pass starts only after the
// current pass returns and the Scheduler signals. Panics inside the loop
// are recovered and the next cycle proceeds; a lost lease stops the daemon.
func (o *Orchestrator) RunForever(ctx context.Context, opts Options, sched Scheduler) error {
	return o.RunForeverDynamic(ctx, func() Options { return opts }, sched)
}

// RunForeverDynamic is RunForever with per-cycle options: optsFn is invoked
// at the start of every cycle, so an event-driven scheduler (e.g.
// WebhookScheduler.NextOptions) can scope each cycle to just the
// repositories its events touched, while periodic sweeps still cover the
// full manifest.
func (o *Orchestrator) RunForeverDynamic(ctx context.Context, optsFn func() Options, sched Scheduler) error {
	if sched == nil {
		return fmt.Errorf("scheduler is required for continuous mode")
	}
	if optsFn == nil {
		return fmt.Errorf("options function is required for continuous mode")
	}
	o.Log.Printf("continuous indexing started")
	for {
		if err := o.runCycleSafely(ctx, optsFn()); err != nil {
			return fmt.Errorf("stopping continuous indexing: %w", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := sched.Wait(ctx); err != nil {
			return err
		}
	}
}

// runCycleSafely runs one indexing cycle, recovering any panic. Its return
// is non-nil only for a lost lease; every other failure is logged and the
// next scheduled cycle is the retry.
func (o *Orchestrator) runCycleSafely(ctx context.Context, opts Options) (err error) {
	defer func() {
		if p := recover(); p != nil {
			o.Log.Printf("cycle panic recovered: %v", p)
			err = nil
		}
	}()
	if o.HealthChecker != nil {
		if herr := o.HealthChecker.VerifyConnectivity(ctx); herr != nil {
			o.Log.Printf("WARNING: health check failed (continuing anyway): %v", herr)
		}
	}
	s, runErr := o.RunOnce(ctx, opts)
	if runErr != nil {
		o.Log.Printf("run aborted: %v", runErr)
	}
	o.LogSummary(s)
	if errors.Is(runErr, errLeaseLost) {
		return runErr
	}
	return nil
}

// reconcileRetired compares the (:Repository) nodes in the graph against the
// configured manifest. A graph repo missing from the config is warned on
// first sight; on a later full run past retirementGrace its graph data is
// deleted and its state reset. Failures are logged and swallowed so
// reconciliation never blocks the indexing run.
func (o *Orchestrator) reconcileRetired(ctx context.Context, configured []Repository) {
	inConfig := make(map[string]bool, len(configured))
	for _, r := range configured {
		inConfig[r.Name] = true
	}

	// A repo back in the config cancels any pending retirement, whether or
	// not the graph listing below succeeds.
	for name, st := range o.Store.All() {
		if inConfig[name] && !st.RetirementWarnedAt.IsZero() {
			st.RetirementWarnedAt = time.Time{}
			if err := o.Store.Set(st); err != nil {
				o.Log.Printf("WARNING: [%s] failed to clear retirement warning: %v", name, err)
			} else {
				o.Log.Printf("[%s] back in config, retirement canceled", name)
			}
		}
	}

	graphRepos, err := o.Retirer.ListRepositoryNames(ctx)
	if err != nil {
		o.Log.Printf("WARNING: retirement reconciliation skipped: %v", err)
		return
	}
	var missing []string
	for _, name := range graphRepos {
		if !inConfig[name] {
			missing = append(missing, name)
		}
	}
	// Mass-retirement guard: more than half the graph missing from the
	// config is treated as a wrong config file, not a retirement - nothing
	// is warned or deleted. To retire that many repos, remove them in
	// batches of less than half the graph, one grace period apart.
	if len(missing) > 1 && len(missing) > len(graphRepos)/2 {
		o.Log.Printf("ERROR: retirement reconciliation skipped: %d of %d repos in the graph are missing from the config (%s); this looks like a wrong config file, not a retirement - no data will be deleted. If intentional, retire in batches of less than half the graph.",
			len(missing), len(graphRepos), strings.Join(missing, ", "))
		return
	}
	now := o.now()
	for _, name := range missing {
		st, _ := o.Store.Get(name)
		if st.RetirementWarnedAt.IsZero() {
			st.Name = name
			st.RetirementWarnedAt = now
			if err := o.Store.Set(st); err != nil {
				o.Log.Printf("WARNING: [%s] failed to persist retirement warning: %v", name, err)
				continue
			}
			o.Log.Printf("WARNING: [%s] is in the graph but not in the config; its graph data will be deleted on a run after %s (re-add it to the config to keep it)", name, now.Add(retirementGrace).Format(time.RFC3339))
			continue
		}
		if now.Sub(st.RetirementWarnedAt) < retirementGrace {
			o.Log.Printf("[%s] retirement pending, deleting after %s", name, st.RetirementWarnedAt.Add(retirementGrace).Format(time.RFC3339))
			continue
		}
		nodes, rels, err := o.Retirer.DeleteRepositoryGraph(ctx, name)
		if err != nil {
			o.Log.Printf("WARNING: [%s] retirement delete failed (will retry next run): %v", name, err)
			continue
		}
		// Reset state so a future re-add re-indexes from scratch.
		if err := o.Store.Set(RepoState{Name: name}); err != nil {
			o.Log.Printf("WARNING: [%s] retired but state reset failed: %v", name, err)
		}
		o.Log.Printf("[%s] retired: deleted %d nodes, %d edges", name, nodes, rels)
	}
}

// IndexOne runs the full sync -> change-detect -> graphify -> import
// pipeline for one repository, persists state, and returns the result. It's
// the entry point RunOnce composes over the configured set, and the one a
// webhook handler or queue consumer would call directly.
//
// Panics in any stage are recovered into a StagePanic-tagged StatusFailed
// result. State persistence failures are logged but not propagated.
func (o *Orchestrator) IndexOne(ctx context.Context, repo Repository, force bool) RepoResult {
	start := o.now()
	prev, _ := o.Store.Get(repo.Name)

	result := RepoResult{
		Name:       repo.Name,
		URL:        repo.URL,
		Branch:     repo.Branch,
		PrevCommit: prev.LastIndexedCommit,
		StartedAt:  start,
	}

	defer func() {
		if p := recover(); p != nil {
			result.Status = StatusFailed
			result.Stage = StagePanic
			result.Error = fmt.Sprintf("panic: %v", p)
			result.Duration = o.now().Sub(start)
			o.persistResult(repo.Name, result)
		}
	}()

	o.runPipeline(ctx, repo, force, prev, start, &result)
	o.persistResult(repo.Name, result)
	o.logResult(result)
	return result
}

func (o *Orchestrator) persistResult(name string, r RepoResult) {
	if r.Canceled {
		// A canceled run does not advance state or the failure counter.
		return
	}
	if err := o.Store.Set(toState(r, o.Store)); err != nil {
		o.Log.Printf("[%s] WARNING: persist state failed: %v", name, err)
	}
}

// runPipeline performs the per-stage work, writing progress directly into
// the caller-provided *RepoResult so a panic mid-stage still leaves partial
// progress (Commit, Nodes) visible to IndexOne's deferred recover.
func (o *Orchestrator) runPipeline(ctx context.Context, repo Repository, force bool, prev RepoState, start time.Time, result *RepoResult) {
	repoPath := filepath.Join(o.WorkDir, "repos", repo.Name)

	o.Log.Printf("[%s] sync %s @ %s", repo.Name, repo.URL, repo.Branch)
	commit, err := o.Syncer.Sync(ctx, repo, repoPath)
	if o.recordFailure(result, StageSync, err, start, ctx) {
		return
	}
	result.Commit = commit

	if ctx.Err() != nil {
		o.markCanceled(result, StageSync, start)
		return
	}

	if !force && prev.LastStatus == StatusSuccess && prev.LastIndexedCommit == commit {
		if prev.SchemaVersion != GraphSchemaVersion {
			o.Log.Printf("[%s] graph schema changed (v%d -> v%d), re-indexing despite unchanged HEAD",
				repo.Name, prev.SchemaVersion, GraphSchemaVersion)
		} else {
			result.Status = StatusSkipped
			result.Reason = fmt.Sprintf("HEAD %s unchanged since %s", commit, prev.LastIndexedAt.Format(time.RFC3339))
			result.Duration = o.now().Sub(start)
			return
		}
	}

	o.Log.Printf("[%s] graphify %s", repo.Name, commit)
	graphPath, err := o.Graphify.Generate(ctx, repoPath)
	if o.recordFailure(result, StageGraphify, err, start, ctx) {
		return
	}

	if ctx.Err() != nil {
		o.markCanceled(result, StageGraphify, start)
		return
	}

	// Run platform extractors and merge their fragments into the file the
	// importer will read. One extractor failing never blocks the others; by
	// default it does block the import (see AllowPartialExtractorErrors), so
	// the sweep cannot delete a failed extractor's last-known-good data.
	importPath := graphPath
	if o.Extractors != nil && len(o.Extractors.Extractors) > 0 {
		o.Log.Printf("[%s] extract (%d extractors)", repo.Name, len(o.Extractors.Extractors))
		// extract.Runner only logs a failure or a warning, never progress - on
		// a huge repo this stage can go silent for minutes the same way
		// graphify's subprocess can, so it gets the same still-running ticker.
		stopTicker := startProgressTicker(progressTickInterval, func(elapsed time.Duration) {
			o.Log.Printf("[%s] extract still running (%s elapsed)", repo.Name, elapsed)
		})
		extResult := o.Extractors.Run(ctx, repoPath, repo.Name)
		stopTicker()
		if len(extResult.Errors) > 0 {
			result.ExtractorErrors = map[string]string{}
			for n, e := range extResult.Errors {
				result.ExtractorErrors[n] = e.Error()
			}
			if !o.AllowPartialExtractorErrors {
				names := make([]string, 0, len(extResult.Errors))
				for n := range extResult.Errors {
					names = append(names, n)
				}
				sort.Strings(names)
				err := fmt.Errorf("extractor(s) failed: %s (set extractors.allow_partial: true to import anyway)", strings.Join(names, ", "))
				if o.recordFailure(result, StageExtract, err, start, ctx) {
					return
				}
			}
			o.Log.Printf("[%s] WARNING: %d extractor(s) failed, allow_partial is set - importing the partial graph anyway", repo.Name, len(extResult.Errors))
		}
		if len(extResult.Fragments) > 0 {
			result.ExtractorStats = map[string]ExtractorStat{}
			for _, f := range extResult.Fragments {
				result.ExtractorStats[f.Extractor] = ExtractorStat{
					Nodes: len(f.Nodes),
					Edges: len(f.Edges),
				}
			}
			mergedPath := filepath.Join(filepath.Dir(graphPath), "graph.merged.json")
			if err := extract.MergeIntoGraphFile(graphPath, mergedPath, extResult.Fragments); err != nil {
				if o.recordFailure(result, StageMerge, err, start, ctx) {
					return
				}
			} else {
				importPath = mergedPath
			}
		}
	}

	if ctx.Err() != nil {
		o.markCanceled(result, StageExtract, start)
		return
	}

	o.Log.Printf("[%s] import %s", repo.Name, importPath)
	sum, err := o.Importer.Run(ctx, repo.Name, commit, importPath, force)
	if o.recordFailure(result, StageImport, err, start, ctx) {
		return
	}

	result.Nodes = sum.NodesTotal
	result.Links = sum.LinksImported
	result.NodesSwept = sum.NodesSwept
	result.EdgesSwept = sum.EdgesSwept
	result.NodesInGraph = sum.NodesInGraph
	result.Mismatch = sum.NodesMismatch()
	if result.Mismatch {
		o.Log.Printf("[%s] WARNING: node-count mismatch - imported %d, Neo4j holds %d (delta %d). Investigate node_key collisions.",
			repo.Name, sum.NodesTotal, sum.NodesInGraph, sum.NodesTotal-sum.NodesInGraph)
	}
	result.SweepResidueNodes = sum.SweepResidueNodes
	result.SweepResidueRels = sum.SweepResidueRels
	result.SweepResidue = sum.HasSweepResidue()
	if result.SweepResidue {
		o.Log.Printf("[%s] ERROR: sweep residue - %d stale nodes, %d stale relationships still present after SweepStale. Sweep logic is broken or a concurrent writer raced this run.",
			repo.Name, result.SweepResidueNodes, result.SweepResidueRels)
	}

	// A mismatch or sweep residue means the graph may not reflect what this
	// run wrote. Fail the repo: state does not advance, and the next cycle
	// retries against last-known-good data.
	if result.Mismatch || result.SweepResidue {
		var reasons []string
		if result.Mismatch {
			reasons = append(reasons, fmt.Sprintf("node-count mismatch (imported %d, graph holds %d)", sum.NodesTotal, sum.NodesInGraph))
		}
		if result.SweepResidue {
			reasons = append(reasons, fmt.Sprintf("sweep residue (%d nodes, %d rels)", result.SweepResidueNodes, result.SweepResidueRels))
		}
		result.Status = StatusFailed
		result.Stage = StageImport
		result.Error = strings.Join(reasons, "; ")
	} else {
		result.Status = StatusSuccess
	}
	result.Duration = o.now().Sub(start)
}

// recordFailure writes a Stage-tagged failure to result, unless ctx was
// canceled - then it is marked Canceled so consecutive_fails is not
// affected by a shutdown. Returns true if err is non-nil.
func (o *Orchestrator) recordFailure(r *RepoResult, stage Stage, err error, start time.Time, ctx context.Context) bool {
	if err == nil {
		return false
	}
	r.Duration = o.now().Sub(start)
	if ctx.Err() != nil {
		o.markCanceled(r, stage, start)
		return true
	}
	r.Status = StatusFailed
	r.Stage = stage
	r.Error = err.Error()
	return true
}

func (o *Orchestrator) markCanceled(r *RepoResult, stage Stage, start time.Time) {
	r.Status = StatusSkipped
	r.Stage = stage
	r.Canceled = true
	r.Reason = "canceled"
	if r.Duration == 0 {
		r.Duration = o.now().Sub(start)
	}
}

// LogSummary writes a human-readable summary block to the orchestrator's logger.
func (o *Orchestrator) LogSummary(s RunSummary) {
	total, success, skipped, failed := s.Counts()
	dur := s.FinishedAt.Sub(s.StartedAt).Round(time.Millisecond)
	if s.FinishedAt.IsZero() {
		dur = 0
	}
	o.Log.Printf("--- indexing summary (%s elapsed) ---", dur)
	o.Log.Printf("  total: %d  success: %d  skipped: %d  failed: %d", total, success, skipped, failed)
	for _, r := range s.Results {
		switch r.Status {
		case StatusSuccess:
			swept := ""
			if r.NodesSwept > 0 || r.EdgesSwept > 0 {
				swept = fmt.Sprintf(", swept %d/%d", r.NodesSwept, r.EdgesSwept)
			}
			o.Log.Printf("  + %s @ %s: %d nodes, %d links (%s%s)", r.Name, shortSHA(r.Commit), r.Nodes, r.Links, r.Duration.Round(time.Millisecond), swept)
		case StatusSkipped:
			o.Log.Printf("  = %s @ %s: %s", r.Name, shortSHA(r.Commit), r.Reason)
		case StatusFailed:
			o.Log.Printf("  ! %s: %s failed: %s", r.Name, r.Stage, r.Error)
		}
	}
}

func (o *Orchestrator) logResult(r RepoResult) {
	switch r.Status {
	case StatusSuccess:
		o.Log.Printf("[%s] success: %d nodes, %d links in %s (swept %d nodes / %d edges)",
			r.Name, r.Nodes, r.Links, r.Duration.Round(time.Millisecond), r.NodesSwept, r.EdgesSwept)
	case StatusSkipped:
		if r.Canceled {
			o.Log.Printf("[%s] canceled during %s", r.Name, r.Stage)
		} else {
			o.Log.Printf("[%s] skipped: %s", r.Name, r.Reason)
		}
	case StatusFailed:
		o.Log.Printf("[%s] FAILED (%s): %s", r.Name, r.Stage, r.Error)
	}
}

func (o *Orchestrator) now() time.Time {
	if o.Clock != nil {
		return o.Clock()
	}
	return time.Now()
}

// toState merges a result into existing state. Existing fields are preserved
// for repos that were skipped, except LastAttemptAt which always advances.
func toState(r RepoResult, store StateStore) RepoState {
	prev, _ := store.Get(r.Name)
	out := prev
	out.Name = r.Name
	now := r.StartedAt.Add(r.Duration)
	out.LastAttemptAt = now
	out.LastStatus = r.Status
	out.LastStage = r.Stage
	out.LastDurationMS = r.Duration.Milliseconds()

	switch r.Status {
	case StatusSuccess:
		out.LastIndexedAt = now
		out.LastIndexedCommit = r.Commit
		out.SchemaVersion = GraphSchemaVersion
		out.LastNodes = r.Nodes
		out.LastLinks = r.Links
		out.LastError = ""
		out.ConsecutiveFails = 0
	case StatusSkipped:
		// Skipped (no-change) and canceled both clear the error counter.
		out.LastError = ""
		out.ConsecutiveFails = 0
	case StatusFailed:
		out.LastError = r.Error
		out.ConsecutiveFails = prev.ConsecutiveFails + 1
	}
	return out
}

func filterRepos(all []Repository, names []string) ([]Repository, error) {
	if len(names) == 0 {
		return all, nil
	}
	wanted := make(map[string]bool, len(names))
	for _, n := range names {
		wanted[n] = true
	}
	out := make([]Repository, 0, len(names))
	for _, r := range all {
		if wanted[r.Name] {
			out = append(out, r)
			delete(wanted, r.Name)
		}
	}
	if len(wanted) > 0 {
		var missing []string
		for n := range wanted {
			missing = append(missing, n)
		}
		return nil, fmt.Errorf("unknown repositories: %v", missing)
	}
	return out, nil
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
