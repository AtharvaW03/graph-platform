package index

import (
	"errors"
	"log"
	"os"
	"path/filepath"
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

func TestEnsureIgnorePatterns(t *testing.T) {
	patterns := []string{"*.tfvars"}

	t.Run("creates file when absent", func(t *testing.T) {
		dir := t.TempDir()
		if err := ensureIgnorePatterns(dir, patterns); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dir, ".graphifyignore"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(got), "*.tfvars") {
			t.Fatalf("pattern missing from created file:\n%s", got)
		}
	})

	t.Run("appends to repo-owned file without clobbering", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".graphifyignore")
		if err := os.WriteFile(path, []byte("node_modules/\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := ensureIgnorePatterns(dir, patterns); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(path)
		s := string(got)
		if !strings.Contains(s, "node_modules/") {
			t.Fatalf("repo-owned entry lost:\n%s", s)
		}
		if !strings.Contains(s, "*.tfvars") {
			t.Fatalf("platform pattern not appended:\n%s", s)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		dir := t.TempDir()
		if err := ensureIgnorePatterns(dir, patterns); err != nil {
			t.Fatal(err)
		}
		first, _ := os.ReadFile(filepath.Join(dir, ".graphifyignore"))
		if err := ensureIgnorePatterns(dir, patterns); err != nil {
			t.Fatal(err)
		}
		second, _ := os.ReadFile(filepath.Join(dir, ".graphifyignore"))
		if string(first) != string(second) {
			t.Fatalf("second run changed the file:\nfirst:\n%s\nsecond:\n%s", first, second)
		}
	})

	t.Run("nil patterns writes nothing", func(t *testing.T) {
		dir := t.TempDir()
		if err := ensureIgnorePatterns(dir, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".graphifyignore")); !os.IsNotExist(err) {
			t.Fatalf("file should not exist, stat err = %v", err)
		}
	})

	t.Run("refuses symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(t.TempDir(), "victim")
		if err := os.WriteFile(target, []byte("precious\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, ".graphifyignore")); err != nil {
			t.Skipf("cannot create symlink on this platform: %v", err)
		}
		if err := ensureIgnorePatterns(dir, patterns); err == nil {
			t.Fatal("expected an error for a symlinked .graphifyignore, got nil")
		}
		got, _ := os.ReadFile(target)
		if string(got) != "precious\n" {
			t.Fatalf("symlink target was modified:\n%s", got)
		}
	})

	t.Run("refuses directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".graphifyignore"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := ensureIgnorePatterns(dir, patterns); err == nil {
			t.Fatal("expected an error for a directory .graphifyignore, got nil")
		}
	})
}

// TestParseGraphifyVersion_StaleSkillWarning: the CLI can print a warning
// containing an OLDER version before the real version line; the parser must
// report the binary's version, not the warning's.
func TestParseGraphifyVersion_StaleSkillWarning(t *testing.T) {
	out := "  warning: skill is from graphify 0.8.44, package is 0.9.12. Run 'graphify install' to update.\ngraphify 0.9.12\n"
	v, ok := parseGraphifyVersion(out)
	if !ok || v != "0.9.12" {
		t.Fatalf("parsed %q ok=%v, want 0.9.12", v, ok)
	}
}

func TestParseGraphifyVersion_LastTokenFallback(t *testing.T) {
	v, ok := parseGraphifyVersion("something 1.2.3 then version: 4.5.6")
	if !ok || v != "4.5.6" {
		t.Fatalf("parsed %q ok=%v, want 4.5.6 (last token wins)", v, ok)
	}
}
