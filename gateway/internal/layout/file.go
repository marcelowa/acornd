package layout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// LockBudget is how long a lock waits for another process. Flock is released when its holder ends, so what a
// caller waits for is a write in progress, and that is a moment; a wait that outlives this budget means the other
// process is wedged, and being told so beats hanging — by the command line, and by every test run, where a hang
// costs the whole timeout to learn nothing.
const LockBudget = 2 * time.Second

const lockRetry = 25 * time.Millisecond

// Lock takes the exclusive lock on path, held across one read-modify-write so the command line and a running
// gateway cannot lose each other's change. The lock is a file beside path, path + ".lock", which holds nothing;
// it exists to be locked. Call the release it answers when the write is done. It does not create path's folder:
// a folder that went away under a running process is reported, not made again empty.
func Lock(path string) (func(), error) { return lock(path, syscall.LOCK_EX) }

// LockShared takes the shared lock on path: readers share it, and a writer waits for them.
func LockShared(path string) (func(), error) { return lock(path, syscall.LOCK_SH) }

// lock asks for the lock without blocking, and retries until the budget is spent. A holder that never lets go is
// somebody else's wedged process, and the refusal says which file it was: the person reading it can act on that,
// and a wait cannot.
func lock(path string, mode int) (func(), error) {
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	release := func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}
	deadline := time.Now().Add(LockBudget)
	for {
		err := syscall.Flock(int(file.Fd()), mode|syscall.LOCK_NB)
		if err == nil {
			return release, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = file.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("another acornd process has been holding %s for over %s: try again", path, LockBudget)
		}
		time.Sleep(lockRetry)
	}
}

// WriteFile replaces path with payload, readable by its owner only. It writes a temp file beside path, syncs it
// and renames it over path, so a reader sees the old file or the new one and never part of each. The temp file's
// name is its own: with one shared name, two writers would write into the same file, and one would rename the
// other's half-written bytes into place.
func WriteFile(path string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	// After the rename this finds nothing; before it, it is the half-written file nobody else will clean up.
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
