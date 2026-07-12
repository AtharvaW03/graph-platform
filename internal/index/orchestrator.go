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
// Neo4j. Bump it whenever a change means previously-indexed repos must be
// re-imported even though their source hasn't moved: the unchanged-HEAD skip
// only applies when a repo's recorded SchemaVersion matches, so a bump rolls
// the migration out automatically on the next cycle.
//
//	v2: HAS_ENTITY ownership edges (replacing repo-membership CONTAINS),
//	    label folded into the content hash, graphify extract --code-only.
//	v3: graphify 0.9.13 output shape - markdown quick-scan/semantic twin
//	    nodes now merge into one, and cross-module reference stubs now
//	    rewire onto their definitions. Both change what a repo's nodes
//	    look like without changing its source, so unchanged-HEAD repos
//	    need one forced re-extract to converge on the new shape.
const GraphSchemaVersion = 3

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
	// the importer reads it. By default an extractor error fails the whole
	// repo closed (see AllowPartialExtractorErrors); either way, per-extractor
	// errors are recorded on the RepoResult for triage.
	Extractors *extract.Runner

	// AllowPartialExtractorErrors, when true, restores the old behavior: an
	// extractor error is logged but the partial graph imports anyway. Default
	// false (fail closed) - see ExtractorsConfig.AllowPartial's doc comment
	// for why that's the safer default.
	AllowPartialExtractorErrors bool

	// HealthChecker, if set, is pinged before each cycle in continuous mode.
	// A failed ping is logged but does not abort - the cycle proceeds and
	// individual stage failures will be recorded per-repo.
	HealthChecker HealthChecker

	// Lease, if set, is renewed before every repo (in both RunOnce and
	// RunForever, which shares RunOnce's loop). Unlike HealthChecker, a
	// failed renewal is fatal to the run - see LeaseRenewer.
	Lease LeaseRenewer
}

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

// LeaseRenewer extends the writer lease. If Orchestrator.Lease is set,
// RunOnce calls Renew before every repo - so a long --all run over many
// repos can't outlive the TTL between the start of the run and whichever
// repo is indexing when it expires. A failed renewal means another writer
// claimed the lease, and the run stops immediately before the next repo -
// a single-writer violation is not something to log and continue past.
type LeaseRenewer interface {
	Renew(ctx context.Context) error
}

// errLeaseLost marks a RunOnce error as a lease renewal failure. RunForever
// checks for it specifically: unlike other RunOnce errors (which just get
// logged before the next scheduled cycle retries), losing the lease means
// someone else is writing now - retrying next cycle would race them, so
// RunForever stops the daemon instead.
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
// stops the loop after. RunOnce never panics; any panic in collaborators is
// recovered, logged, and recorded on the run.
//
// When a Lease is configured, it's renewed before every repo, not just once
// at the top - a long --all run over many repos must not outlive the lease
// TTL between repo 1 and repo 200. A renewal failure means another writer
// took over; the loop stops immediately, before that next repo, and RunOnce
// returns an error alongside whatever results were already collected.
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

// RunForever loops RunOnce on the configured Scheduler until ctx is canceled.
// Cycles never overlap: the next pass only starts after the current pass
// returns AND the Scheduler signals. A panic inside the loop is recovered
// and the next cycle proceeds - the daemon never dies on a recoverable bug.
//
// Lease renewal happens inside RunOnce's per-repo loop, not here - that
// covers the first repo of every cycle too, so a separate top-of-cycle
// renewal would only be a redundant extra round-trip. A lost lease, though,
// must stop the daemon rather than wait for the next cycle to retry -
// runCycleSafely surfaces that one case and RunForever returns instead of
// looping.
func (o *Orchestrator) RunForever(ctx context.Context, opts Options, sched Scheduler) error {
	if sched == nil {
		return fmt.Errorf("scheduler is required for continuous mode")
	}
	o.Log.Printf("continuous indexing started")
	for {
		if err := o.runCycleSafely(ctx, opts); err != nil {
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

// runCycleSafely runs one indexing cycle, recovering any panic so the daemon
// never dies on a recoverable bug. Its return is non-nil only for a lost
// lease - every other RunOnce failure (including a recovered panic) is
// logged here and swallowed, same as before, since the next scheduled cycle
// is the retry.
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
		// Leave state untouched so a SIGINT doesn't pollute consecutive_fails.
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
	// graphify update writes inside the repo tree (graphify-out/graph.json),
	// which survives `git reset --hard` and gives it a prior-state cache.

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
	// importer will read. One extractor failing never blocks the others, but
	// by default it does block the import (see AllowPartialExtractorErrors):
	// importing a partial graph would let the sweep delete the failed
	// extractor's last-known-good data, trading a stale-but-correct graph for
	// a fresher-but-wrong one.
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

	// A mismatch or sweep residue means the graph may not actually reflect
	// what this run just did - "imported OK" isn't trustworthy anymore. Fail
	// the repo instead of reporting success: state doesn't advance, so the
	// next cycle retries against last-known-good data instead of building on
	// top of a graph that might already be wrong.
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
// canceled - then it's marked Canceled instead so consecutive_fails isn't
// polluted by an operator-initiated shutdown. Returns true if err is
// non-nil, so the caller knows to bail out.
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
			// A mismatch or sweep residue always fails the repo now (see
			// runPipeline), so a StatusSuccess result never carries either -
			// nothing to annotate here.
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
