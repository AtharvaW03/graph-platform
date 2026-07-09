package graphify

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// defaultMaxGraphBytes caps the graph.json size Load will read; a JSON decode
// expands to several times the file size in memory. Overridable via
// GRAPHIFY_MAX_GRAPH_BYTES (same env var graphify uses).
const defaultMaxGraphBytes = 2 << 30 // 2 GiB

// Load reads and parses a graph.json file, refusing files over the configured
// limit (GRAPHIFY_MAX_GRAPH_BYTES, default 2 GiB).
func Load(path string) (*Graph, error) {
	limit := maxGraphBytes()

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if fi, statErr := f.Stat(); statErr == nil && fi.Size() > limit {
		return nil, fmt.Errorf("graph.json is %d bytes, exceeds limit of %d (raise GRAPHIFY_MAX_GRAPH_BYTES)", fi.Size(), limit)
	}

	// Read one byte past the limit so an overflow is detectable rather than a
	// silent truncation, in case Stat under-reported.
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("graph.json exceeds limit of %d bytes (raise GRAPHIFY_MAX_GRAPH_BYTES)", limit)
	}

	var graph Graph
	if err := json.Unmarshal(data, &graph); err != nil {
		return nil, err
	}
	return &graph, nil
}

// maxGraphBytes returns the size limit from GRAPHIFY_MAX_GRAPH_BYTES (plain
// bytes or a KB/MB/GB suffix), or the default when unset or unparseable.
func maxGraphBytes() int64 {
	if v := os.Getenv("GRAPHIFY_MAX_GRAPH_BYTES"); v != "" {
		if n, err := parseByteSize(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxGraphBytes
}

// parseByteSize parses "2GB", "700MB", "1048576", etc. into a byte count.
func parseByteSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "GB"):
		mult, s = 1<<30, strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "MB"):
		mult, s = 1<<20, strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "KB"):
		mult, s = 1<<10, strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "B"):
		s = strings.TrimSuffix(s, "B")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, err
	}
	return n * mult, nil
}
