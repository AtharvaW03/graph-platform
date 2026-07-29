package index

import (
	"context"
	"os"
	"path/filepath"
)

// The platform retains no source code between runs. A repository's working
// copy exists only while it is being parsed, and is deleted as soon as the
// pipeline finishes - success or failure. What survives is the extractor's
// AST cache, which holds structural metadata only (ids, labels, file paths,
// line numbers), never file contents, and is what keeps re-extraction
// incremental after the checkout is thrown away.
//
// Layout:
//
//	workdir/repos/<name>/                 ephemeral checkout
//	workdir/repos/<name>/graphify-out/    extractor output, during the run
//	workdir/cache/<name>/graphify-out/    persisted cache, between runs
//
// The cache is moved (renamed, same filesystem) rather than copied, so the
// hand-off costs nothing regardless of repository size.

// cacheSubdir is the only part of the extractor's output kept between runs.
// Everything else (graph.json, graph.merged.json, dated report directories)
// is intermediate: once the import succeeds the data lives in Neo4j, and
// keeping derived copies of source-shaped output on disk serves nothing.
const cacheSubdir = "cache"

// graphifyOutDir is where the extractor writes, relative to the checkout.
const graphifyOutDir = "graphify-out"

// cachePath is where a repo's extractor cache lives between runs.
func (o *Orchestrator) cachePath(repoName string) string {
	return filepath.Join(o.WorkDir, "cache", repoName, graphifyOutDir)
}

// restoreCache moves a previously stashed extractor cache into the fresh
// checkout so extraction stays incremental. A missing cache is normal (first
// index) and never an error.
func (o *Orchestrator) restoreCache(repoName, repoPath string) {
	src := o.cachePath(repoName)
	if _, err := os.Stat(src); err != nil {
		return
	}
	dst := filepath.Join(repoPath, graphifyOutDir)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		o.Log.Printf("[%s] WARNING: cache restore failed (extraction will be a full pass): %v", repoName, err)
		return
	}
	// A leftover output dir in a fresh checkout shouldn't happen, but if it
	// does the stashed cache is the authoritative one.
	_ = os.RemoveAll(dst)
	if err := os.Rename(src, dst); err != nil {
		o.Log.Printf("[%s] WARNING: cache restore failed (extraction will be a full pass): %v", repoName, err)
	}
}

// releaseCheckout stashes the extractor cache and removes the working copy.
// Called on every pipeline exit, so no source survives a run - including a
// failed one, where leaving a checkout behind would be the easiest way for
// code to quietly accumulate on the volume.
//
// KeepCheckout suppresses the delete for debugging a failing extraction;
// it is off by default and announced loudly at startup when on.
func (o *Orchestrator) releaseCheckout(repoName, repoPath string) {
	if _, err := os.Stat(repoPath); err != nil {
		return // never cloned (skipped repo), nothing to release
	}
	if o.KeepCheckout {
		return
	}
	o.stashCache(repoName, repoPath)
	if err := os.RemoveAll(repoPath); err != nil {
		// Loud: a checkout that outlives its run is exactly what this is
		// here to prevent, and a silent failure would erode the guarantee.
		o.Log.Printf("[%s] WARNING: failed to remove working copy at %s: %v", repoName, repoPath, err)
	}
}

// stashCache moves the extractor cache out of the checkout and prunes every
// other artifact, so what persists is structural metadata only.
func (o *Orchestrator) stashCache(repoName, repoPath string) {
	outDir := filepath.Join(repoPath, graphifyOutDir)
	srcCache := filepath.Join(outDir, cacheSubdir)
	if _, err := os.Stat(srcCache); err != nil {
		return // nothing to keep
	}
	dstOut := o.cachePath(repoName)
	dstCache := filepath.Join(dstOut, cacheSubdir)

	if err := os.MkdirAll(dstOut, 0o755); err != nil {
		o.Log.Printf("[%s] WARNING: cache stash failed (next extraction will be a full pass): %v", repoName, err)
		return
	}
	_ = os.RemoveAll(dstCache)
	if err := os.Rename(srcCache, dstCache); err != nil {
		o.Log.Printf("[%s] WARNING: cache stash failed (next extraction will be a full pass): %v", repoName, err)
	}
}

// unchangedRemoteHead reports the remote branch tip when it matches what was
// last indexed, so the caller can skip without cloning. It returns ok=false
// whenever anything is uncertain - the syncer can't check remotes, the
// remote is unreachable, state is missing, or the schema version moved -
// because the safe answer to "did this change?" is always "assume yes".
func (o *Orchestrator) unchangedRemoteHead(ctx context.Context, repo Repository, force bool, prev RepoState) (string, bool) {
	if force || prev.LastStatus != StatusSuccess || prev.LastIndexedCommit == "" {
		return "", false
	}
	if prev.SchemaVersion != GraphSchemaVersion {
		return "", false
	}
	checker, ok := o.Syncer.(RemoteHeadChecker)
	if !ok {
		return "", false
	}
	head, err := checker.RemoteHead(ctx, repo)
	if err != nil {
		o.Log.Printf("[%s] remote HEAD check failed, falling back to a full sync: %v", repo.Name, err)
		return "", false
	}
	if head != prev.LastIndexedCommit {
		return "", false
	}
	return head, true
}

// RemoteHeadChecker is the optional Syncer capability that makes
// clone-free change detection possible. *GitSyncer implements it; a Syncer
// that doesn't simply loses the optimization, never correctness.
type RemoteHeadChecker interface {
	RemoteHead(ctx context.Context, repo Repository) (string, error)
}
