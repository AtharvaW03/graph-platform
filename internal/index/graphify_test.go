package index

import (
	"strings"
	"testing"
)

// TestGraphifyEnvDefaultsAndOverride verifies the subprocess env gets the
// headless-indexer defaults when absent, and that an operator-set value is
// never clobbered (deployment env wins).
func TestGraphifyEnvDefaultsAndOverride(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"GRAPHIFY_MAX_GRAPH_BYTES=8GB", // operator override - must survive
	}
	got := map[string]string{}
	for _, kv := range graphifyEnv(base) {
		if i := strings.IndexByte(kv, '='); i > 0 {
			got[kv[:i]] = kv[i+1:]
		}
	}

	if got["PATH"] != "/usr/bin" {
		t.Errorf("inherited PATH lost: %q", got["PATH"])
	}
	// Defaults applied when absent.
	if got["GRAPHIFY_VIZ_NODE_LIMIT"] != "0" {
		t.Errorf("GRAPHIFY_VIZ_NODE_LIMIT = %q, want 0", got["GRAPHIFY_VIZ_NODE_LIMIT"])
	}
	if got["PYTHONHASHSEED"] != "0" {
		t.Errorf("PYTHONHASHSEED = %q, want 0", got["PYTHONHASHSEED"])
	}
	// Operator override preserved, not overwritten by the 2GB default.
	if got["GRAPHIFY_MAX_GRAPH_BYTES"] != "8GB" {
		t.Errorf("operator override clobbered: GRAPHIFY_MAX_GRAPH_BYTES = %q, want 8GB", got["GRAPHIFY_MAX_GRAPH_BYTES"])
	}
	// No duplicate key for the overridden var.
	n := 0
	for _, kv := range graphifyEnv(base) {
		if strings.HasPrefix(kv, "GRAPHIFY_MAX_GRAPH_BYTES=") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("GRAPHIFY_MAX_GRAPH_BYTES appears %d times, want 1", n)
	}
}
