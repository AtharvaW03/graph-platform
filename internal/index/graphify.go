package index

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ExecGraphifier runs an external extractor command (by default the
// `graphify` CLI, invoked as `graphify update <repo_path>`) and resolves the
// produced graph.json inside the repository's own graphify-out/ directory.
//
// The {repo_path} placeholder is substituted into Args; the command runs
// with repoPath as its working directory. Prior output is never deleted -
// `update` consumes it to do an incremental run - and the post-run file must
// exist and be non-empty for the run to count as successful.
type ExecGraphifier struct {
	Command    string
	Args       []string
	OutputFile string // relative to repoPath; default graphify-out/graph.json
	Timeout    time.Duration
	Stderr     io.Writer
}

func NewExecGraphifier(cfg GraphifyConfig, stderr io.Writer) *ExecGraphifier {
	return &ExecGraphifier{
		Command:    cfg.Command,
		Args:       cfg.Args,
		OutputFile: cfg.OutputFile,
		Timeout:    cfg.Timeout,
		Stderr:     stderr,
	}
}

// Generate runs the configured extractor command for the repo at repoPath
// and returns the absolute path of the resulting graph.json. The output
// path is OutputFile resolved relative to the absolute repoPath - graphify
// `update` writes into the repo, so there is no separate out directory.
//
// Subprocess stdout and stderr are routed to the configured Stderr writer
// so the daemon's protocol streams (the MCP stdio transport, in particular)
// are never corrupted by extractor chatter.
func (g *ExecGraphifier) Generate(ctx context.Context, repoPath string) (string, error) {
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("abs repo path: %w", err)
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
