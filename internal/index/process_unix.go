//go:build unix

package index

import (
	"os/exec"
	"syscall"
)

// setupProcessGroup puts the subprocess in its own process group and signals
// the whole group on cancellation, so helper processes a git or graphify
// subprocess spawns (ssh, git-credential-*) don't get orphaned.
func setupProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	prev := cmd.Cancel
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			// Negative pid targets the whole process group.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		if prev != nil {
			return prev()
		}
		return nil
	}
}
