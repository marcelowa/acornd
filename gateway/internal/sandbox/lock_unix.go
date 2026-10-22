//go:build unix

package sandbox

import (
	"os"
	"syscall"
)

// lockFile runs fn while holding an exclusive lock on path, which it creates. The lock is the
// file's own flock: nothing is written to it, and the kernel drops it when the process ends, so a
// crashed pull never leaves a stale lock behind.
func lockFile(path string, fn func() error) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }()
	return fn()
}
