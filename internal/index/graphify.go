package index

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ExecGraphifier runs an external extractor command (by default the
// `graphify` CLI, invoked as `graphify extract <repo_path> --code-only
// --force`) and resolves the produced graph.json inside the repository's own
// graphify-out/ directory.
//
// The {repo_path} placeholder is substituted into Args; the command runs
// with repoPath as its working directory. The post-run output file must
// exist and be non-empty for the run to count as successful.
type ExecGraphifier struct {
	Command    string
	Args       []string
	OutputFile string // relative to repoPath; default graphify-out/graph.json
	Timeout    time.Duration
	Stderr     io.Writer

	// IgnorePatterns are written into each repo's .graphifyignore before the
	// extractor runs. graphify only reads ignore files from the corpus root,
	// so a platform-wide exclusion (tfvars, most importantly) has to be
	// injected into every checkout - a pattern committed to THIS repo's
	// ignore file protects nothing but this repo. Repo-owned ignore entries
	// are preserved; injection is append-only and idempotent.
	IgnorePatterns []string
}

func NewExecGraphifier(cfg GraphifyConfig, stderr io.Writer) *ExecGraphifier {
	return &ExecGraphifier{
		Command:        cfg.Command,
		Args:           cfg.Args,
		OutputFile:     cfg.OutputFile,
		Timeout:        cfg.Timeout,
		Stderr:         stderr,
		IgnorePatterns: cfg.IgnorePatterns,
	}
}

// Generate runs the configured extractor command for the repo at repoPath
// and returns the absolute path of the resulting graph.json. The output
// path is OutputFile resolved relative to the absolute repoPath - graphify
// writes into the repo, so there is no separate out directory.
//
// Subprocess stdout and stderr are routed to the configured Stderr writer
// so the daemon's protocol streams (the MCP stdio transport, in particular)
// are never corrupted by extractor chatter.
func (g *ExecGraphifier) Generate(ctx context.Context, repoPath string) (string, error) {
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("abs repo path: %w", err)
	}

	if err := ensureIgnorePatterns(absRepo, g.IgnorePatterns); err != nil {
		return "", fmt.Errorf("inject ignore patterns: %w", err)
	}

	args := make([]string, len(g.Args))
	for i, a := range g.Args {
		a = strings.ReplaceAll(a, "{repo_path}", absRepo)
		args[i] = a
	}

	cmdCtx := ctx
	if g.Timeout > 0 {
		var cancel context.CancelFunc
		cmdCtx, cancel = context.WithTimeout(ctx, g.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(cmdCtx, g.Command, args...)
	cmd.Dir = absRepo
	cmd.Env = graphifyEnv(os.Environ())
	setupProcessGroup(cmd)

	stderrSink := g.Stderr
	if stderrSink == nil {
		stderrSink = os.Stderr
	}
	tail := &tailWriter{max: 4096}
	cmd.Stdout = io.MultiWriter(stderrSink, tail)
	cmd.Stderr = io.MultiWriter(stderrSink, tail)

	// graphify can sit silent for minutes on a big repo; without this an
	// operator watching the terminal can't tell it apart from a hang. Args
	// carries {repo_path} already substituted, not the repo's platform name,
	// so this derives a short label from the checkout dir instead of adding a
	// repo-name parameter to the Graphifier interface.
	repoLabel := filepath.Base(absRepo)
	stopTicker := startProgressTicker(progressTickInterval, func(elapsed time.Duration) {
		fmt.Fprintf(stderrSink, "[%s] graphify still running (%s elapsed)\n", repoLabel, elapsed)
	})
	defer stopTicker()

	if err := cmd.Run(); err != nil {
		if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("%s timed out after %s\n--- output tail ---\n%s", g.Command, g.Timeout, tail.String())
		}
		if errors.Is(cmdCtx.Err(), context.Canceled) {
			return "", fmt.Errorf("%s canceled\n--- output tail ---\n%s", g.Command, tail.String())
		}
		return "", fmt.Errorf("%s %s: %w\n--- output tail ---\n%s", g.Command, strings.Join(args, " "), err, tail.String())
	}

	produced := filepath.Join(absRepo, g.OutputFile)
	info, err := os.Stat(produced)
	if err != nil {
		return "", fmt.Errorf("expected output not found at %s: %w\n--- output tail ---\n%s", produced, err, tail.String())
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("expected output at %s is empty\n--- output tail ---\n%s", produced, tail.String())
	}
	return produced, nil
}

// graphifyVersionRe extracts a lenient semver token from `graphify --version`
// output, which varies between "graphify 0.9.9" and a bare "0.9.9".
var (
	// graphifyVersionLineRe matches the canonical version statement
	// ("graphify 0.9.12"). It must be tried before the loose match: the CLI
	// can print a stale-skill warning FIRST that contains a different,
	// older version ("warning: skill is from graphify 0.8.44, package is
	// 0.9.12."), and grabbing the first semver in the output would read the
	// warning's version, not the binary's.
	graphifyVersionLineRe = regexp.MustCompile(`(?m)^graphify\s+(\d+\.\d+(?:\.\d+)?)\s*$`)
	graphifyVersionRe     = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)
)

// parseGraphifyVersion pulls the version substring out of --version output:
// the canonical "graphify X.Y.Z" line when present, else the LAST
// version-shaped token (later output supersedes warnings printed before it).
// Returns ("", false) when nothing version-shaped is found.
func parseGraphifyVersion(output string) (string, bool) {
	if m := graphifyVersionLineRe.FindStringSubmatch(output); m != nil {
		return m[1], true
	}
	all := graphifyVersionRe.FindAllString(output, -1)
	if len(all) == 0 {
		return "", false
	}
	return all[len(all)-1], true
}

// checkGraphifyVersion is the pure decision step for CheckGraphifyVersion,
// factored out so the mismatch/unknown logic is testable without a
// subprocess. runErr is whatever running `<command> --version` returned;
// output is its combined stdout+stderr.
func checkGraphifyVersion(expected, output string, runErr error, log *log.Logger) error {
	if runErr != nil {
		if expected == "" {
			log.Printf("graphify version: unknown (--version failed: %v)", runErr)
			return nil
		}
		return fmt.Errorf("graphify version check failed: could not run --version (%v); expected %s", runErr, expected)
	}

	detected, ok := parseGraphifyVersion(output)
	if !ok {
		if expected == "" {
			log.Printf("graphify version: unknown (no version string in --version output)")
			return nil
		}
		return fmt.Errorf("graphify version check failed: no version string in --version output; expected %s", expected)
	}

	if expected == "" {
		log.Printf("graphify version: %s", detected)
		return nil
	}
	if detected != expected {
		return fmt.Errorf("graphify version mismatch: detected %s, expected %s", detected, expected)
	}
	log.Printf("graphify version: %s (matches expected)", detected)
	return nil
}

// CheckGraphifyVersion runs `<command> --version` once and hard-fails on a
// mismatch against cfg.ExpectedVersion. Call it once at startup, before any
// repo is processed - native operator drift (a graphify upgraded outside the
// pinned Docker image) should stop the run, not corrupt a partial one.
func CheckGraphifyVersion(ctx context.Context, cfg GraphifyConfig, log *log.Logger) error {
	cmd := exec.CommandContext(ctx, cfg.Command, "--version")
	out, err := cmd.CombinedOutput()
	return checkGraphifyVersion(cfg.ExpectedVersion, string(out), err, log)
}

// ensureIgnorePatterns appends any missing patterns to <repo>/.graphifyignore
// so platform-wide exclusions apply to every indexed checkout, not just repos
// that committed their own ignore file. Existing repo-owned content is
// preserved (graphify merges ignore semantics, extra patterns only ever
// exclude more). Idempotent: patterns already present are not duplicated.
// The syncer's fetch+reset wipes the appended lines each cycle, so this runs
// before every extraction.
func ensureIgnorePatterns(repoPath string, patterns []string) error {
	if len(patterns) == 0 {
		return nil
	}
	path := filepath.Join(repoPath, ".graphifyignore")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	present := make(map[string]bool)
	for _, line := range strings.Split(string(existing), "\n") {
		present[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, p := range patterns {
		if p = strings.TrimSpace(p); p != "" && !present[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	var b strings.Builder
	b.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	b.WriteString("# added by the indexer (platform-wide exclusions)\n")
	for _, p := range missing {
		b.WriteString(p + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// graphifyEnv returns the daemon's environment plus headless-indexer
// defaults, applied only where the operator hasn't already set them:
//   - GRAPHIFY_VIZ_NODE_LIMIT=0: skip the HTML viz render, since only
//     graph.json is consumed downstream.
//   - PYTHONHASHSEED=0: deterministic community assignment across cycles.
//   - GRAPHIFY_MAX_GRAPH_BYTES=2GB: raise graphify's default 512 MiB cap so
//     a large repo's incremental read doesn't hard-fail.
func graphifyEnv(base []string) []string {
	seen := make(map[string]bool, len(base))
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i > 0 {
			seen[kv[:i]] = true
		}
	}
	out := append([]string(nil), base...)
	for _, d := range []struct{ key, val string }{
		{"GRAPHIFY_VIZ_NODE_LIMIT", "0"},
		{"PYTHONHASHSEED", "0"},
		{"GRAPHIFY_MAX_GRAPH_BYTES", "2GB"},
	} {
		if !seen[d.key] {
			out = append(out, d.key+"="+d.val)
		}
	}
	return out
}

// tailWriter buffers the last `max` bytes written so subprocess output can be
// quoted in error messages without growing unbounded. Safe for concurrent
// writes since cmd.Stdout/Stderr both feed it via MultiWriter.
type tailWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(p) >= t.max {
		t.buf.Reset()
		t.buf.Write(p[len(p)-t.max:])
		return len(p), nil
	}
	if t.buf.Len()+len(p) > t.max {
		overflow := t.buf.Len() + len(p) - t.max
		t.buf.Next(overflow)
	}
	t.buf.Write(p)
	return len(p), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(t.buf.String())
}
