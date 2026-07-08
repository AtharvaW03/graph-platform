package graphify

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// defaultMaxGraphBytes caps the graph.json size Load will read into memory.
// A JSON decode expands to several times the file size in live Go structs, so
// an unbounded read of a pathologically large graph.json is an OOM risk. This
// mirrors graphify's own GRAPHIFY_MAX_GRAPH_BYTES knob (and honors the same
// env var), so raising the limit for a big monorepo is a single setting on both
// the extractor subprocess and the importer.
const defaultMaxGraphBytes = 2 << 30 // 2 GiB

// Load reads and parses a graph.json file, refusing files larger than the
// configured limit (GRAPHIFY_MAX_GRAPH_BYTES, default 2 GiB) rather than risking
// an out-of-memory decode.
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

	// LimitReader is a belt-and-suspenders backstop in case Stat under-reports
	// (e.g. a file still being written): read one byte past the limit so we can
	// detect an overflow rather than silently truncating the JSON.
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

// maxGraphBytes returns the graph.json size limit, honoring GRAPHIFY_MAX_GRAPH_BYTES
// (accepts a plain byte count or a KB/MB/GB suffix, matching graphify) and
// falling back to the default when unset or unparseable.
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
