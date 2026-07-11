// Package importer loads a graph.json into Neo4j. Both the importer CLI and the
// indexer service call into it.
package importer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"time"

	"graph-platform/internal/graphify"
)

// newRunID returns a token unique to one import run. Every node/edge the run
// writes is stamped with it so SweepStale can drop anything it didn't write -
// including orphans a same-commit re-index leaves behind when node keys change.
func newRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(b[:])
}

// Neo4jClient is the subset of *neo4j.Client the importer uses. Defined here so
// the import sequence can be tested without a live database.
type Neo4jClient interface {
	EnsureConstraints(ctx context.Context) error
	MergeRepository(ctx context.Context, repo string) error
	ImportNodes(ctx context.Context, repo, commit, runID string, nodes []graphify.Node, rewriteAll bool) (idToKey map[string]string, sharedKeys map[string]bool, labelCounts map[string]int, err error)
	ImportLinks(ctx context.Context, repo, commit, runID string, links []graphify.Link, idToKey map[string]string, sharedKeys map[string]bool, rewriteAll bool) (map[string]int, int, int, error)
	SweepStale(ctx context.Context, repo, commit, runID string) (int, int, error)
	VerifySweepClean(ctx context.Context, repo, runID string) (staleNodes, staleRels int, err error)
	CountEntitiesForRepo(ctx context.Context, repo string) (int, error)
}

// Stage names, passed to the Progress callback and used as error prefixes.
const (
	StageConstraints = "constraints"
	StageRepo        = "merge repository"
	StageNodes       = "import nodes"
	StageLinks       = "import links"
	StageSweep       = "sweep stale"
	StageVerifySweep = "verify sweep"
	StageVerify      = "verify count"
	StageLoad        = "load graph"
)

// Options configures a single import run.
//
// A non-empty Commit stamps every node/edge and enables the post-import sweep
// of stale data from prior commits. An empty Commit skips both (static-graph
// mode for the CLI). RewriteAll forces a full property rewrite regardless of
// content hash - the repair path for manual property drift; the static CLI
// always sets it to keep its pre-existing always-rewrite behavior. Progress,
// if set, is called at the start of each stage.
type Options struct {
	Repo       string
	Commit     string
	GraphPath  string
	RewriteAll bool
	Progress   func(stage string)
}

// Summary reports the results of a run. Counts reflect what was actually
// written to Neo4j (post-allowlist labels, post-skip links). Hyperedges are
// not imported; SkippedHyperedges records how many were dropped.
type Summary struct {
	Repo              string
	Commit            string
	NodesTotal        int
	LinksTotal        int
	LinksImported     int
	LabelCounts       map[string]int
	RelationCounts    map[string]int
	SkippedUnknown    int
	SkippedDangling   int
	SkippedHyperedges int
	NodesSwept        int
	EdgesSwept        int
	// NodesInGraph is the :Entity count Neo4j holds for this repo after the
	// import completes (post-sweep). Comparing it against NodesTotal surfaces
	// silent data loss, e.g. from node_key collisions.
	NodesInGraph int
	// SweepResidueNodes/Rels count repo-owned nodes/edges that SweepStale
	// should have removed but didn't - zero on a healthy run. Nonzero means
	// the sweep itself is broken, or a concurrent writer raced it.
	SweepResidueNodes int
	SweepResidueRels  int
}

// HasSweepResidue reports whether VerifySweepClean found anything the sweep
// should have removed. Only meaningful with a commit; sweep doesn't run
// (and residue is never populated) in static-graph mode.
func (s *Summary) HasSweepResidue() bool {
	return s.SweepResidueNodes > 0 || s.SweepResidueRels > 0
}

// NodesMismatch reports whether the input node count and Neo4j's final :Entity
// count disagree. Only meaningful with a commit; in static-graph mode the count
// is cumulative and a mismatch is expected.
func (s *Summary) NodesMismatch() bool {
	return s.Commit != "" && s.NodesTotal != s.NodesInGraph
}

// SortedLabels returns the label names in stable order.
func (s *Summary) SortedLabels() []string {
	return sortedKeys(s.LabelCounts)
}

// SortedRelations returns the relation names in stable order.
func (s *Summary) SortedRelations() []string {
	return sortedKeys(s.RelationCounts)
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// LoadGraph parses a graph.json into a *graphify.Graph. Exposed so callers can
// pre-load a graph and pass it to RunWithGraph.
func LoadGraph(path string) (*graphify.Graph, error) {
	g, err := graphify.Load(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", StageLoad, err)
	}
	return g, nil
}

// Run loads the graph at opts.GraphPath and imports it via client. It is the
// canonical entry point for the import pipeline.
func Run(ctx context.Context, client Neo4jClient, opts Options) (*Summary, error) {
	g, err := LoadGraph(opts.GraphPath)
	if err != nil {
		return nil, err
	}
	return RunWithGraph(ctx, client, opts.Repo, opts.Commit, opts.RewriteAll, g, opts.Progress)
}

// RunWithGraph imports an already-loaded graph. Same as Run without the parse.
func RunWithGraph(ctx context.Context, client Neo4jClient, repo, commit string, rewriteAll bool, g *graphify.Graph, progress func(string)) (*Summary, error) {
	if progress == nil {
		progress = func(string) {}
	}

	progress(StageConstraints)
	if err := client.EnsureConstraints(ctx); err != nil {
		return nil, fmt.Errorf("%s: %w", StageConstraints, err)
	}

	progress(StageRepo)
	if err := client.MergeRepository(ctx, repo); err != nil {
		return nil, fmt.Errorf("%s: %w", StageRepo, err)
	}

	// runID marks this run's writes so the sweep can drop everything else. Only
	// set in sweep mode; the static-graph CLI path leaves it empty.
	runID := ""
	if commit != "" {
		runID = newRunID()
	}

	progress(StageNodes)
	idToKey, sharedKeys, labelCounts, err := client.ImportNodes(ctx, repo, commit, runID, g.Nodes, rewriteAll)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", StageNodes, err)
	}

	progress(StageLinks)
	relCounts, skippedUnknown, skippedDangling, err := client.ImportLinks(ctx, repo, commit, runID, g.Links, idToKey, sharedKeys, rewriteAll)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", StageLinks, err)
	}

	var nodesSwept, edgesSwept, residueNodes, residueRels int
	if commit != "" {
		progress(StageSweep)
		ns, es, err := client.SweepStale(ctx, repo, commit, runID)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", StageSweep, err)
		}
		nodesSwept, edgesSwept = ns, es

		progress(StageVerifySweep)
		rn, rr, err := client.VerifySweepClean(ctx, repo, runID)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", StageVerifySweep, err)
		}
		residueNodes, residueRels = rn, rr
	}

	progress(StageVerify)
	nodesInGraph, err := client.CountEntitiesForRepo(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", StageVerify, err)
	}

	return &Summary{
		Repo:              repo,
		Commit:            commit,
		NodesTotal:        len(g.Nodes),
		LinksTotal:        len(g.Links),
		LinksImported:     len(g.Links) - skippedUnknown - skippedDangling,
		LabelCounts:       labelCounts,
		RelationCounts:    relCounts,
		SkippedUnknown:    skippedUnknown,
		SkippedDangling:   skippedDangling,
		SkippedHyperedges: len(g.HyperEdges),
		NodesSwept:        nodesSwept,
		EdgesSwept:        edgesSwept,
		NodesInGraph:      nodesInGraph,
		SweepResidueNodes: residueNodes,
		SweepResidueRels:  residueRels,
	}, nil
}
