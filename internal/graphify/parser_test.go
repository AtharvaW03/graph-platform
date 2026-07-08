package graphify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseByteSize(t *testing.T) {
	cases := map[string]int64{
		"1024":   1024,
		"2GB":    2 << 30,
		"700MB":  700 << 20,
		"4kb":    4 << 10,
		" 512B ": 512,
	}
	for in, want := range cases {
		got, err := parseByteSize(in)
		if err != nil || got != want {
			t.Errorf("parseByteSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := parseByteSize("notanumber"); err == nil {
		t.Error("expected error for non-numeric size")
	}
}

func TestLoadRejectsOversizeGraph(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "graph.json")
	if err := os.WriteFile(p, []byte(`{"nodes":[],"links":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Normal load under the default limit works.
	if _, err := Load(p); err != nil {
		t.Fatalf("valid small graph.json should load: %v", err)
	}

	// A tiny cap makes the same file exceed the limit and be refused.
	t.Setenv("GRAPHIFY_MAX_GRAPH_BYTES", "5B")
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Errorf("expected size-limit rejection, got %v", err)
	}
}
