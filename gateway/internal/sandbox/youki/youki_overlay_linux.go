//go:build linux

package youki

import (
	"os"
	"os/exec"
	"path/filepath"
)

// overlaySupported mounts a throwaway overlay through the same overlay-run helper a sandbox uses.
// A machine that cannot mount it (no CAP_SYS_ADMIN for the gateway user, an old kernel, a
// filesystem without overlayfs) keeps the copy fallback; the probe's answer is cached by the
// caller.
func (y *Youki) overlaySupported() bool {
	if err := os.MkdirAll(y.StateDir(), 0o700); err != nil {
		return false
	}
	dir, err := os.MkdirTemp(y.StateDir(), "overlay-probe-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	base := filepath.Join(dir, "base")
	upper := filepath.Join(dir, "upper")
	work := filepath.Join(dir, "work")
	root := filepath.Join(dir, "root")
	for _, path := range []string{base, upper, work, root} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return false
		}
	}
	self := y.supervisor
	if self == "" {
		var err error
		self, err = os.Executable()
		if err != nil {
			return false
		}
	}
	command := exec.Command(self,
		"internal", "overlay-run",
		"--base", base,
		"--upper", upper,
		"--work", work,
		"--root", root,
		"--", "/bin/true")
	return command.Run() == nil
}
