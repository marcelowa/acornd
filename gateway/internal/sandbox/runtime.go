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

// Where the supervisor lives inside a sandbox, and where its socket does. The guest paths are
// short on purpose: a Unix socket address is limited, and the host side of the same socket
// carries the workspace's name. Both container engines run the same supervisor, so the paths
// are the same on both sides of either engine's bind.
const (
	guestSupervisor = "/usr/local/bin/acornd"
	guestRunDir     = "/run/acornd"
	guestSocketPath = guestRunDir + "/s.sock"
)

// socketLimit is the length a Unix socket path may have (Linux allows 107 characters and a
// terminator). Both sides of one socket have to fit, and the host side is the long one, which is
// why the folder is named by a short hash rather than by the sandbox's whole name.
const socketLimit = 106

func socketDirFor(dataDir, workspace string) (string, error) {
	runtime, err := RuntimeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(runtime, shortHash(dataDir+"\x00"+workspace))
	if err := ensureOwnedDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// sandboxSocketPaths is the session's runtime folder and the two sockets in it. The socket lives
// there rather than in the store because a Unix socket path has a hard length and this way the
// length is the same wherever the install lives; the limit is checked here because the failure
// without it is a supervisor that never answers. ADR 0024 lost a day to that limit.
func sandboxSocketPaths(dataDir, workspace string) (run, socket string, err error) {
	run, err = socketDirFor(dataDir, workspace)
	if err != nil {
		return "", "", err
	}
	socket = filepath.Join(run, "s.sock")
	for _, path := range []string{socket, guestSocketPath} {
		if len(path) > socketLimit {
			return "", "", fmt.Errorf("sandbox: the supervisor's socket path is too long for a Unix socket: %s is %d characters and %d is the limit (shorten XDG_RUNTIME_DIR, or the workspace name)", path, len(path), socketLimit)
		}
	}
	return run, socket, nil
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
