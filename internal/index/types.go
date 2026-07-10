// Package index implements the indexing service: it discovers repositories,
// keeps them cloned and in sync, runs graphify on changed repos, and feeds
// the produced graph.json into the Neo4j importer. JobSource, Syncer,
// Graphifier, and StateStore are all interfaces, so webhooks, job queues, or
// parallel workers can be added later by swapping an implementation.
package index

import (
	"context"
	"time"
)

// Status is the outcome of one repository's indexing attempt.
type Status string

const (
	StatusSuccess Status = "success"
	StatusSkipped Status = "skipped"
	StatusFailed  Status = "failed"
)

// Stage identifies where in the pipeline an outcome was produced. When a
// failure occurs, Stage records which step blew up so operators can triage.
type Stage string

const (
	StageSync     Stage = "sync"
	StageGraphify Stage = "graphify"
	StageExtract  Stage = "extract"
	StageMerge    Stage = "merge"
	StageImport   Stage = "import"
	StagePanic    Stage = "panic"
)

// Repository is one entry in the indexer's repo manifest. Fields are
// YAML-tagged so the configuration file is readable by ops.
type Repository struct {
	Name   string `yaml:"name"`
	URL    string `yaml:"url"`
	Branch string `yaml:"branch"`
}

// RepoState is the persisted indexing record for one repository, used across
// restarts to decide (via LastIndexedCommit) whether a repo has changed
// since the last successful run.
type RepoState struct {
	Name              string    `json:"name"`
	LastAttemptAt     time.Time `json:"last_attempt_at,omitempty"`
	LastIndexedAt     time.Time `json:"last_indexed_at,omitempty"`
	LastIndexedCommit string    `json:"last_indexed_commit,omitempty"`
	LastStatus        Status    `json:"last_status,omitempty"`
	LastStage         Stage     `json:"last_stage,omitempty"`
	LastError         string    `json:"last_error,omitempty"`
	LastDurationMS    int64     `json:"last_duration_ms,omitempty"`
	LastNodes         int       `json:"last_nodes,omitempty"`
	LastLinks         int       `json:"last_links,omitempty"`
	ConsecutiveFails  int       `json:"consecutive_fails,omitempty"`
}

// RepoResult captures everything that happened to a single repo during a run.
// It is the unit of return from the per-repo pipeline.
type RepoResult struct {
	Name       string
	URL        string
	Branch     string
	Status     Status
	Stage      Stage
	Commit     string
	PrevCommit string
	Reason     string
	Error      string
	Nodes      int
	Links      int
	NodesSwept int
	EdgesSwept int
	// NodesInGraph is Neo4j's actual :Entity count for the repo after import.
	// A mismatch against Nodes signals silent data loss, e.g. node_key collisions.
	NodesInGraph int
	Mismatch     bool
	// SweepResidueNodes/Rels are what VerifySweepClean found left behind after
	// SweepStale ran - stale data the sweep should have removed but didn't.
	// Nonzero means the sweep is broken or a concurrent writer raced this run.
	SweepResidueNodes int
	SweepResidueRels  int
	SweepResidue      bool
	// ExtractorStats holds per-extractor node/edge counts.
	ExtractorStats map[string]ExtractorStat
	// ExtractorErrors maps extractor name to error message; other extractors
	// still run and their fragments still merge in.
	ExtractorErrors map[string]string
	// Canceled is true when ctx cancellation, not the repo, stopped the run;
	// state persistence is skipped so consecutive_fails isn't polluted.
	Canceled  bool
	StartedAt time.Time
	Duration  time.Duration
}

// ExtractorStat is a per-extractor node/edge count summary.
type ExtractorStat struct {
	Nodes int
	Edges int
}

// RunSummary is the aggregate of one indexing pass over a set of repositories.
type RunSummary struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Results    []RepoResult
}

// Counts breaks down a RunSummary by outcome for logging.
func (s RunSummary) Counts() (total, success, skipped, failed int) {
	total = len(s.Results)
	for _, r := range s.Results {
		switch r.Status {
		case StatusSuccess:
			success++
		case StatusSkipped:
			skipped++
		case StatusFailed:
			failed++
		}
	}
	return
}

// JobSource yields the set of repositories to index. The YAML-backed
// implementation reads a config file; other implementations could drain a
// queue or listen to webhooks.
type JobSource interface {
	Repositories(ctx context.Context) ([]Repository, error)
}

// Syncer clones or updates a repo at dest and returns the current HEAD
// commit, so the orchestrator can compare it against previously-indexed state.
type Syncer interface {
	Sync(ctx context.Context, repo Repository, dest string) (commit string, err error)
}

// Graphifier produces or updates a graph.json for a repo at repoPath and
// returns the resolved absolute path of the result.
type Graphifier interface {
	Generate(ctx context.Context, repoPath string) (graphPath string, err error)
}

// StateStore persists per-repo indexing state. The JSON-file implementation
// is enough for single-process operation.
type StateStore interface {
	Get(name string) (RepoState, bool)
	Set(state RepoState) error
	All() map[string]RepoState
}
