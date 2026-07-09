// Package extract defines the extractor framework. Extractors scan a synced
// repo checkout and emit graphify-compatible {nodes, edges} fragments. The
// orchestrator runs every extractor, merges their fragments into graphify's
// graph.json, and feeds the result to the Neo4j importer.
//
// A new extractor is one struct plus one Extract method.
package extract

import (
	"context"
	"fmt"
)

// Confidence labels follow graphify's three-tier model. EXTRACTED edges
// always have confidence_score 1.0.
const (
	ConfidenceExtracted = "EXTRACTED"
	ConfidenceInferred  = "INFERRED"
	ConfidenceAmbiguous = "AMBIGUOUS"
)

// FragmentNode mirrors a graphify-format node; the field tags match graphify's
// node-link serialization.
type FragmentNode struct {
	ID             string         `json:"id"`
	Label          string         `json:"label"`
	NormLabel      string         `json:"norm_label,omitempty"`
	Origin         string         `json:"_origin,omitempty"` // matches graphify's _origin field
	Type           string         `json:"type,omitempty"`
	Ecosystem      string         `json:"ecosystem,omitempty"`
	FileType       string         `json:"file_type,omitempty"`
	SourceFile     string         `json:"source_file,omitempty"`
	SourceLocation string         `json:"source_location,omitempty"`
	Community      int            `json:"community,omitempty"`
	CommunityName  string         `json:"community_name,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// FragmentEdge mirrors a graphify-format edge. Relation uses graphify's
// lowercase verb form (e.g. "depends_on"); the importer maps it to Neo4j's
// UPPER_SNAKE_CASE.
type FragmentEdge struct {
	Source          string  `json:"source"`
	Target          string  `json:"target"`
	Relation        string  `json:"relation"`
	Confidence      string  `json:"confidence"`
	ConfidenceScore float64 `json:"confidence_score,omitempty"`
	Weight          float64 `json:"weight,omitempty"`
	SourceFile      string  `json:"source_file,omitempty"`
	SourceLocation  string  `json:"source_location,omitempty"`
	Context         string  `json:"context,omitempty"`
}

// Fragment is one extractor's contribution to the unified graph.
type Fragment struct {
	Extractor string         `json:"-"` // set by the runner
	Nodes     []FragmentNode `json:"nodes"`
	Edges     []FragmentEdge `json:"edges"`
	Warnings  []string       `json:"-"` // non-fatal issues

	// byID indexes Nodes for AddNode's dedup. Built lazily so a Fragment from
	// a literal or JSON unmarshal still works.
	byID map[string]int
}

// NewFragment returns an empty fragment tagged with the extractor's name.
func NewFragment(extractor string) *Fragment {
	return &Fragment{Extractor: extractor}
}

// AddNode appends a node, defaulting common fields and stamping _origin to
// mark platform-emitted nodes apart from graphify's AST nodes. It is
// idempotent per ID: a repeat call merges metadata into the existing node
// (existing values win), so extractors can emit the same hub from several
// sites without tracking a seen-set.
func (f *Fragment) AddNode(n FragmentNode) {
	if n.Origin == "" {
		n.Origin = "platform"
	}
	if n.NormLabel == "" {
		n.NormLabel = n.Label
	}
	if f.byID == nil {
		f.byID = make(map[string]int, len(f.Nodes))
		for i := range f.Nodes {
			f.byID[f.Nodes[i].ID] = i
		}
	}
	if i, ok := f.byID[n.ID]; ok {
		mergeNodeInPlace(&f.Nodes[i], n)
		return
	}
	f.byID[n.ID] = len(f.Nodes)
	f.Nodes = append(f.Nodes, n)
}

// mergeNodeInPlace fills empty fields of dst from src; populated fields on
// dst are preserved (first-write wins for top-level fields). Metadata is
// merged key-by-key with the same rule.
func mergeNodeInPlace(dst *FragmentNode, src FragmentNode) {
	if dst.Label == "" {
		dst.Label = src.Label
	}
	if dst.NormLabel == "" {
		dst.NormLabel = src.NormLabel
	}
	if dst.Type == "" {
		dst.Type = src.Type
	}
	if dst.Ecosystem == "" {
		dst.Ecosystem = src.Ecosystem
	}
	if dst.FileType == "" {
		dst.FileType = src.FileType
	}
	if dst.SourceFile == "" {
		dst.SourceFile = src.SourceFile
	}
	if dst.SourceLocation == "" {
		dst.SourceLocation = src.SourceLocation
	}
	if dst.Community == 0 {
		dst.Community = src.Community
	}
	if dst.CommunityName == "" {
		dst.CommunityName = src.CommunityName
	}
	if dst.Origin == "" {
		dst.Origin = src.Origin
	}
	if dst.Metadata == nil {
		dst.Metadata = map[string]any{}
	}
	for k, v := range src.Metadata {
		if _, exists := dst.Metadata[k]; !exists {
			dst.Metadata[k] = v
		}
	}
}

// AddEdge appends an edge, defaulting confidence + confidence_score so an
// extractor that omits them still produces a valid graphify fragment.
func (f *Fragment) AddEdge(e FragmentEdge) {
	if e.Confidence == "" {
		e.Confidence = ConfidenceExtracted
	}
	if e.ConfidenceScore == 0 {
		switch e.Confidence {
		case ConfidenceExtracted:
			e.ConfidenceScore = 1.0
		case ConfidenceInferred:
			e.ConfidenceScore = 0.75
		case ConfidenceAmbiguous:
			e.ConfidenceScore = 0.5
		}
	}
	if e.Weight == 0 {
		e.Weight = 1.0
	}
	f.Edges = append(f.Edges, e)
}

// Warn records a non-fatal issue without aborting extraction.
func (f *Fragment) Warn(msg string) {
	f.Warnings = append(f.Warnings, msg)
}

// Empty reports whether the fragment carries no graph data. Empty fragments
// are dropped before merge so they don't clutter the unified file.
func (f *Fragment) Empty() bool {
	return len(f.Nodes) == 0 && len(f.Edges) == 0
}

// Extractor reads files under repoPath and returns the entities it found for
// the repo. Implementations run independently (no shared state), are read-only
// on disk, and one returning an error never aborts the others.
type Extractor interface {
	Name() string
	Extract(ctx context.Context, repoPath, repoName string) (*Fragment, error)
}

// Validate checks the minimal schema invariants: non-empty node IDs and
// labels, non-empty edge endpoints and relations, and a known confidence tier.
// Edges to node IDs absent from this fragment are allowed - they may resolve
// against another fragment or graphify's own nodes, and anything still dangling
// is skipped at import time.
func (f *Fragment) Validate() error {
	if f == nil {
		return fmt.Errorf("nil fragment")
	}
	for i, n := range f.Nodes {
		if n.ID == "" {
			return fmt.Errorf("node[%d]: missing id", i)
		}
		if n.Label == "" {
			return fmt.Errorf("node[%d] (%s): missing label", i, n.ID)
		}
	}
	for i, e := range f.Edges {
		if e.Source == "" || e.Target == "" {
			return fmt.Errorf("edge[%d]: missing source/target", i)
		}
		if e.Relation == "" {
			return fmt.Errorf("edge[%d] (%s -> %s): missing relation", i, e.Source, e.Target)
		}
		if e.Confidence != ConfidenceExtracted && e.Confidence != ConfidenceInferred && e.Confidence != ConfidenceAmbiguous {
			return fmt.Errorf("edge[%d] (%s -> %s): invalid confidence %q", i, e.Source, e.Target, e.Confidence)
		}
	}
	return nil
}
