//go:build unix

package index

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// LockWorkDir takes an exclusive advisory lock on <dir>/.indexer.lock so two
// indexer processes against the same workdir can't race on state.json,
// clones, or graphify output. Keep the returned *os.File open for the
// process lifetime; closing it releases the lock.
func LockWorkDir(dir string) (*os.File, error) {
	path := filepath.Join(dir, ".indexer.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open workdir lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("another indexer is already running against workdir %s", dir)
		}
		return nil, fmt.Errorf("lock workdir %s: %w", dir, err)
	}
	return f, nil
}
