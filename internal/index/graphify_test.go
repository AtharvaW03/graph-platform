package index

import (
	"errors"
	"log"
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

func TestParseGraphifyVersion(t *testing.T) {
	cases := []struct {
		output string
		want   string
		wantOK bool
	}{
		{"graphify 0.9.9", "0.9.9", true},
		{"0.9.9", "0.9.9", true},
		{"graphify version 0.9.9\n", "0.9.9", true},
		{"graphify 1.2", "1.2", true},
		{"graphify: command not found", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := parseGraphifyVersion(c.output)
		if got != c.want || ok != c.wantOK {
			t.Errorf("parseGraphifyVersion(%q) = (%q, %v), want (%q, %v)", c.output, got, ok, c.want, c.wantOK)
		}
	}
}

// discardLogger swallows output so tests don't spam stderr.
func discardLogger() *log.Logger {
	return log.New(nilWriter{}, "", 0)
}

type nilWriter struct{}

func (nilWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestCheckGraphifyVersionLogic(t *testing.T) {
	logger := discardLogger()

	// No expected_version configured: unknown or detected, always continues.
	if err := checkGraphifyVersion("", "graphify 0.9.9", nil, logger); err != nil {
		t.Errorf("no expected version, detected ok: got error %v", err)
	}
	if err := checkGraphifyVersion("", "garbage", nil, logger); err != nil {
		t.Errorf("no expected version, undetectable: got error %v", err)
	}
	if err := checkGraphifyVersion("", "", errors.New("boom"), logger); err != nil {
		t.Errorf("no expected version, subprocess error: got error %v", err)
	}

	// expected_version set: match passes, mismatch and unknown fail.
	if err := checkGraphifyVersion("0.9.9", "graphify 0.9.9", nil, logger); err != nil {
		t.Errorf("expected matches detected: got error %v", err)
	}
	if err := checkGraphifyVersion("0.9.9", "graphify 0.9.10", nil, logger); err == nil {
		t.Error("expected mismatch to fail, got nil error")
	}
	if err := checkGraphifyVersion("0.9.9", "garbage", nil, logger); err == nil {
		t.Error("expected undetectable version to fail when expected_version is set")
	}
	if err := checkGraphifyVersion("0.9.9", "", errors.New("boom"), logger); err == nil {
		t.Error("expected subprocess error to fail when expected_version is set")
	}
}
