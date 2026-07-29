package index

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GitSyncer is the default Syncer: it shells out to the local `git` binary,
// using whatever auth the host's git is already configured for. Subprocesses
// run with credential prompts disabled so a missing key fails fast instead of
// hanging; on unix, cancellation kills the whole process group so a
// long-running clone or fetch can't be orphaned.
type GitSyncer struct {
	Timeout time.Duration
}

func NewGitSyncer(cfg GitConfig) *GitSyncer {
	return &GitSyncer{Timeout: cfg.Timeout}
}

// Sync clones repo at dest if absent, fetches+resets to origin/<branch> if
// present, and returns the resulting HEAD commit.
func (g *GitSyncer) Sync(ctx context.Context, repo Repository, dest string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("ensure parent: %w", err)
	}

	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("stat repo: %w", err)
		}
		if err := g.clone(ctx, repo, dest); err != nil {
			return "", err
		}
	} else {
		if err := g.update(ctx, repo, dest); err != nil {
			return "", err
		}
	}

	return g.head(ctx, dest)
}

func (g *GitSyncer) clone(ctx context.Context, repo Repository, dest string) error {
	// A non-empty dir that isn't a git repo is likely a partial prior clone;
	// refuse to clobber it.
	if entries, err := os.ReadDir(dest); err == nil && len(entries) > 0 {
		return fmt.Errorf("clone target %s is non-empty and not a git repo; remove it manually before retrying", dest)
	}
	// Shallow single-branch clone: only the branch tip is needed, and full
	// history across dozens of repos would be wasted disk and time.
	args := []string{"clone", "--depth", "1", "--single-branch", "--branch", repo.Branch, "--", repo.URL, dest}
	if _, err := g.run(ctx, "", "git", args...); err != nil {
		return fmt.Errorf("clone %s: %w", repo.URL, err)
	}
	return nil
}

func (g *GitSyncer) update(ctx context.Context, repo Repository, dest string) error {
	// Cheap sanity check catches a half-broken .git dir early, with a clearer
	// error than a deeper command would give.
	if _, err := g.run(ctx, dest, "git", "rev-parse", "--git-dir"); err != nil {
		return fmt.Errorf("corrupt clone at %s (rm and re-run to heal): %w", dest, err)
	}
	// Refuse to sync if the remote URL changed underneath us - surface it
	// instead of silently updating against the wrong repo.
	out, err := g.run(ctx, dest, "git", "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("read remote: %w", err)
	}
	current := strings.TrimSpace(out)
	if current != repo.URL {
		return fmt.Errorf("remote mismatch at %s: configured %q, on-disk %q", dest, repo.URL, current)
	}
	if _, err := g.run(ctx, dest, "git", "fetch", "--prune", "origin", repo.Branch); err != nil {
		return fmt.Errorf("fetch %s: %w", repo.Branch, err)
	}
	if _, err := g.run(ctx, dest, "git", "reset", "--hard", "origin/"+repo.Branch); err != nil {
		return fmt.Errorf("reset to origin/%s: %w", repo.Branch, err)
	}
	return nil
}

// RemoteHead returns the branch tip's SHA straight from the remote, with no
// local clone and nothing written to disk. It is what lets an unchanged
// repository be skipped without ever materializing its source: the platform
// keeps no working copy between runs, so the usual fetch-and-compare would
// mean re-downloading a whole tree just to learn nothing changed.
//
// A remote that cannot be reached (or a branch that does not exist) returns
// an error; callers fall back to a full sync rather than treating it as
// "unchanged", so an unreachable remote can never silently freeze a repo at
// an old commit.
func (g *GitSyncer) RemoteHead(ctx context.Context, repo Repository) (string, error) {
	ref := "refs/heads/" + repo.Branch
	out, err := g.run(ctx, "", "git", "ls-remote", "--", repo.URL, ref)
	if err != nil {
		return "", fmt.Errorf("ls-remote %s: %w", repo.URL, err)
	}
	// Output is "<sha>\t<ref>" per matching ref; an empty result means the
	// branch is absent, which must not read as "nothing changed".
	line := strings.TrimSpace(out)
	if line == "" {
		return "", fmt.Errorf("branch %q not found on %s", repo.Branch, repo.URL)
	}
	sha, _, found := strings.Cut(line, "\t")
	if !found || len(sha) < 7 {
		return "", fmt.Errorf("unexpected ls-remote output for %s: %q", repo.URL, line)
	}
	return strings.TrimSpace(sha), nil
}

func (g *GitSyncer) head(ctx context.Context, dest string) (string, error) {
	out, err := g.run(ctx, dest, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func (g *GitSyncer) run(ctx context.Context, workdir, name string, args ...string) (string, error) {
	cmdCtx := ctx
	if g.Timeout > 0 {
		var cancel context.CancelFunc
		cmdCtx, cancel = context.WithTimeout(ctx, g.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(cmdCtx, name, args...)
	if workdir != "" {
		cmd.Dir = workdir
	}
	// Disable credential prompts so a missing key fails fast instead of hanging.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10",
	)
	setupProcessGroup(cmd) // unix: own pgid so cancellation kills children too
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("%s %s: timed out after %s", name, strings.Join(args, " "), g.Timeout)
		}
		if errors.Is(cmdCtx.Err(), context.Canceled) {
			return "", fmt.Errorf("%s %s: canceled", name, strings.Join(args, " "))
		}
		return "", fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
