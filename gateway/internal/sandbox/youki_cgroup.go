package sandbox

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The budget a youki sandbox gets is a cgroup: memory.max, memory.swap.max and cpu.max under the
// absolute cgroupsPath the OCI configuration names. Creating that cgroup is the part the runtime
// cannot do for a rootless user, because the controllers have to be enabled in the parent's
// cgroup.subtree_control by whoever owns it. The probe is real — it creates the parent and
// enables the controllers — because a cgroup that exists without the memory controller is a
// budget that does not bind, which is the failure this release exists to stop.
const (
	cgroupMount = "/sys/fs/cgroup"
	// cgroupChild is the cgroup the sandboxes are children of, at the cgroup root. An absolute
	// cgroupsPath selects youki's cgroupfs manager; a relative one selects systemd and needs a
	// session bus a sandbox does not have (ADR 0022 §2 rule 3). The root is the one a process
	// that can write /sys/fs/cgroup owns; a non-delegated systemd user cannot write it, and
	// Usable refuses with the reason rather than failing inside youki.
	cgroupChild = "acornd.slice"
)

// cgroupParentPath is the cgroup the sandboxes are children of. It is absolute; the runtime's
// cgroupsPath is this plus the sandbox's name.
func cgroupParentPath() string {
	return path.Join("/", cgroupChild)
}

// cgroupPathFor is the absolute cgroupsPath one sandbox's runtime creates.
func cgroupPathFor(name string) string {
	return path.Join(cgroupParentPath(), name)
}

// cgroupDir is a cgroup path as the filesystem sees it.
func cgroupDir(cgroupPath string) string {
	return filepath.Join(cgroupMount, strings.TrimPrefix(cgroupPath, "/"))
}

// probeCgroup answers whether this machine lets a youki sandbox have a budget. On a systemd host
// a non-delegated user cannot write cgroup.subtree_control; the refusal names the missing
// capability rather than failing later inside youki with a sentence about a cgroup setup.
func probeCgroup() error {
	if _, err := os.Stat(filepath.Join(cgroupMount, "cgroup.controllers")); err != nil {
		return fmt.Errorf("this machine has no cgroup v2 mount, so a youki sandbox cannot have a budget: %w", err)
	}
	dir := cgroupDir(cgroupParentPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("this machine does not let the sandbox create its cgroup at %s: %w (a systemd host gives a user a delegated unit, not the cgroup root; run acornd where it can write this path, or delegate a cgroup at the root)", dir, err)
	}
	if err := enableControllers(dir, "memory", "cpu"); err != nil {
		return fmt.Errorf("this machine does not delegate the memory and cpu controllers to %s: %w (a systemd host gives a user a delegated unit, not the cgroup root; run acornd where it can write this path, or delegate a cgroup at the root)", dir, err)
	}
	return nil
}

// enableControllers turns on the controllers a child cgroup needs, and only the ones that are
// not already on. Writing a controller that is enabled is not a no-op on every kernel, and a
// second probe on a machine that is already set up should cost a read.
func enableControllers(dir string, controllers ...string) error {
	path := filepath.Join(dir, "cgroup.subtree_control")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	enabled := map[string]bool{}
	for _, field := range strings.Fields(string(raw)) {
		enabled[field] = true
	}
	var missing []string
	for _, controller := range controllers {
		if !enabled[controller] {
			missing = append(missing, "+"+controller)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return os.WriteFile(path, []byte(strings.Join(missing, " ")), 0o644)
}
