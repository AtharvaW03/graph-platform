package extract

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// MergeIntoGraphFile merges fragments into an existing graphify-format
// graph.json and writes a unified file for the importer. Node IDs are unioned
// (duplicates collapse, missing fields filled in); edges are appended. The
// write is atomic (temp + rename).
func MergeIntoGraphFile(srcPath, dstPath string, fragments []*Fragment) error {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read graph file: %w", err)
	}

	// Decode into a map to preserve every top-level field graphify emitted,
	// including ones our Go types don't model.
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("parse graph file: %w", err)
	}

	existingNodes, _ := envelope["nodes"].([]any)
	existingLinks, _ := envelope["links"].([]any)

	byID := make(map[string]int, len(existingNodes))
	for i, n := range existingNodes {
		nm, ok := n.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := nm["id"].(string); id != "" {
			byID[id] = i
		}
	}

	for _, frag := range fragments {
		if frag == nil {
			continue
		}
		for _, fn := range frag.Nodes {
			nm := nodeToMap(fn)
			if idx, exists := byID[fn.ID]; exists {
				// Existing wins on populated fields; fragment fills the gaps.
				prev, _ := existingNodes[idx].(map[string]any)
				for k, v := range nm {
					if !hasMeaningfulValue(prev[k]) {
						prev[k] = v
					}
				}
				existingNodes[idx] = prev
			} else {
				existingNodes = append(existingNodes, nm)
				byID[fn.ID] = len(existingNodes) - 1
			}
		}
		for _, fe := range frag.Edges {
			existingLinks = append(existingLinks, edgeToMap(fe))
		}
	}

	envelope["nodes"] = existingNodes
	envelope["links"] = existingLinks

	// No indentation: this is a machine-read intermediate, and indenting a
	// large graph.json inflates the encode allocation and file size.
	out, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode merged graph: %w", err)
	}
	dir := filepath.Dir(dstPath)
	tmp, err := os.CreateTemp(dir, ".graph-merge-*.json")
	if err != nil {
		return fmt.Errorf("create merge tmp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write merge tmp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync merge tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close merge tmp: %w", err)
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		return fmt.Errorf("rename merge tmp: %w", err)
	}
	tmpPath = ""
	return nil
}

// WriteFragment writes a single fragment as a standalone graphify-format
// node-link JSON file (debugging / merge-graphs compatibility).
func WriteFragment(path string, frag *Fragment) error {
	envelope := map[string]any{
		"directed":   false,
		"multigraph": false,
		"graph":      map[string]any{"hyperedges": []any{}},
		"nodes":      fragmentNodes(frag),
		"links":      fragmentLinks(frag),
		"hyperedges": []any{},
	}
	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func fragmentNodes(f *Fragment) []map[string]any {
	if f == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(f.Nodes))
	for _, n := range f.Nodes {
		out = append(out, nodeToMap(n))
	}
	return out
}

func fragmentLinks(f *Fragment) []map[string]any {
	if f == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(f.Edges))
	for _, e := range f.Edges {
		out = append(out, edgeToMap(e))
	}
	return out
}

func nodeToMap(n FragmentNode) map[string]any {
	m := map[string]any{
		"id":    n.ID,
		"label": n.Label,
	}
	if n.NormLabel != "" {
		m["norm_label"] = n.NormLabel
	}
	if n.Origin != "" {
		m["_origin"] = n.Origin
	}
	if n.Type != "" {
		m["type"] = n.Type
	}
	if n.Ecosystem != "" {
		m["ecosystem"] = n.Ecosystem
	}
	if n.FileType != "" {
		m["file_type"] = n.FileType
	}
	if n.SourceFile != "" {
		m["source_file"] = n.SourceFile
	}
	if n.SourceLocation != "" {
		m["source_location"] = n.SourceLocation
	}
	if n.Community != 0 {
		m["community"] = n.Community
	}
	if n.CommunityName != "" {
		m["community_name"] = n.CommunityName
	}
	if len(n.Metadata) > 0 {
		m["metadata"] = n.Metadata
	}
	return m
}

func edgeToMap(e FragmentEdge) map[string]any {
	m := map[string]any{
		"source":   e.Source,
		"target":   e.Target,
		"relation": e.Relation,
	}
	if e.Confidence != "" {
		m["confidence"] = e.Confidence
	}
	if e.ConfidenceScore != 0 {
		m["confidence_score"] = e.ConfidenceScore
	}
	if e.Weight != 0 {
		m["weight"] = e.Weight
	}
	if e.SourceFile != "" {
		m["source_file"] = e.SourceFile
	}
	if e.SourceLocation != "" {
		m["source_location"] = e.SourceLocation
	}
	if e.Context != "" {
		m["context"] = e.Context
	}
	return m
}

func hasMeaningfulValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	default:
		return true
	}
}
