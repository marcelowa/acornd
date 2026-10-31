package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// RuntimeDir is where a sandbox's sockets live while it runs: a folder that exists for the life of
// a session, on a path short enough for a Unix socket to name.
//
// It is derived, not configured. `$XDG_RUNTIME_DIR` is the standard answer for "this session's
// scratch space" and is already short; without it (a system service, an SSH session, a container)
// the fallback is this user's own folder under the system's temp directory. Both are places that
// disappear when the user logs out, which is right: a socket is not state.
func RuntimeDir() (string, error) {
	if base := os.Getenv("XDG_RUNTIME_DIR"); base != "" {
		return filepath.Join(base, "acornd"), nil
	}
	return filepath.Join(os.TempDir(), "acornd-"+strconv.Itoa(os.Getuid())), nil
}

// socketDir is the folder one sandbox's socket goes in: per install and per workspace, because two
// installs on one machine can run the same workspace path and must not share a socket.
func (b *Bwrap) socketDir(workspace string) (string, error) {
	runtime, err := RuntimeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(runtime, shortHash(b.dataDir+"\x00"+workspace))
	if err := ensureOwnedDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// ensureOwnedDir creates a folder that only this user owns and can enter. A directory in a shared
// temp location is the classic place for someone else to be waiting, so an existing one that is a
// symlink or belongs to another account is refused rather than used.
func ensureOwnedDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("sandbox: %s is not a folder", dir)
	}
	if uid := ownerOf(info); uid != nil && *uid != os.Getuid() {
		return fmt.Errorf("sandbox: %s belongs to another account (%d); refusing to put a socket there", dir, *uid)
	}
	return nil
}
