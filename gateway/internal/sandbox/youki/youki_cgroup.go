package youki

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// The budget a youki sandbox gets is a cgroup: memory.max, memory.swap.max and cpu.max under the
// absolute cgroupsPath the OCI configuration names. Creating that cgroup is the part the runtime
// cannot do for a rootless user, because the controllers have to be enabled in the parent's
// cgroup.subtree_control by whoever owns it. The probe is real — it creates the parent and
// enables the controllers — because a cgroup that exists without the memory controller is a
// budget that does not bind, which is the failure this release exists to stop.
var cgroupMount = "/sys/fs/cgroup"

const (
	// cgroupChild is the cgroup the sandboxes are children of. An absolute cgroupsPath selects
	// youki's cgroupfs manager; a relative one selects systemd and needs a session bus a sandbox
	// does not have (ADR 0022 §2 rule 3).
	cgroupChild = "acornd.slice"
)

// cgroupParentPath is the cgroup the sandboxes are children of before the probe has chosen one.
// It is absolute; the runtime's cgroupsPath is this plus the sandbox's name.
func cgroupParentPath() string {
	return path.Join("/", cgroupChild)
}

// cgroupPathFor is the absolute cgroupsPath one sandbox's runtime creates.
func (y *Youki) cgroupPathFor(name string) string {
	parent := y.cgroupParent
	if parent == "" {
		parent = cgroupParentPath()
	}
	return path.Join(parent, name)
}

// cgroupDir is a cgroup path as the filesystem sees it.
func cgroupDir(cgroupPath string) string {
	return filepath.Join(cgroupMount, strings.TrimPrefix(cgroupPath, "/"))
}

// currentCgroup is this process's cgroup v2 path, or "" when it cannot be read. A cgroup
// namespace reports "/" for a delegated root, which is the right answer inside it.
func currentCgroup() string {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "0::")
		if !ok {
			continue
		}
		return path.Clean(strings.TrimSpace(rest))
	}
	return ""
}

// currentCgroupPath is what the probe reads. It is a variable so a test can answer for a machine
// whose cgroup differs from this process's, without a cgroup namespace.
var currentCgroupPath = currentCgroup

// cgroupCandidates is where a sandbox's parent can live, best first. The first is the delegated
// cgroup above this process: on a systemd user service the process is the service itself or its
// `init` subgroup, and the sibling `acornd.slice` under the service's slice is the one the user
// owns. The second is a child of the current cgroup, for a delegated cgroup that has no
// processes. The root is the last answer, for a non-systemd machine where acornd can write it.
func cgroupCandidates(current string) []string {
	var candidates []string
	add := func(candidate string) {
		if candidate == "" || slices.Contains(candidates, candidate) {
			return
		}
		candidates = append(candidates, candidate)
	}
	if current != "" && current != "/" && current != "." {
		add(path.Join(path.Dir(current), cgroupChild))
		add(path.Join(current, cgroupChild))
	}
	add(path.Join("/", cgroupChild))
	return candidates
}

// probeCgroup answers whether this machine lets a youki sandbox have a budget, and records the
// parent it found. On a systemd host a non-delegated user cannot write cgroup.subtree_control; the
// refusal names every candidate it tried rather than failing later inside youki.
func (y *Youki) probeCgroup() error {
	if _, err := os.Stat(filepath.Join(cgroupMount, "cgroup.controllers")); err != nil {
		return fmt.Errorf("this machine has no cgroup v2 mount, so a youki sandbox cannot have a budget: %w", err)
	}
	var failures []string
	for _, parent := range cgroupCandidates(currentCgroupPath()) {
		if err := prepareCgroup(parent); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", parent, err))
			continue
		}
		y.cgroupParent = parent
		return nil
	}
	return fmt.Errorf("this machine does not delegate a cgroup to the sandbox, so a youki sandbox cannot have a budget (tried %s); on systemd run acornd in a unit with Delegate=yes and DelegateSubgroup=init, or in a non-systemd cgroup root you own", strings.Join(failures, "; "))
}

// prepareCgroup makes one candidate usable: the controllers are enabled in its parent, then in
// the candidate itself. Enabling a controller that is already on is a read, because writing it
// twice is not a no-op on every kernel.
func prepareCgroup(parent string) error {
	base := path.Dir(parent)
	if err := enableControllers(cgroupDir(base), "memory", "cpu"); err != nil {
		return fmt.Errorf("the parent %s: %w", base, err)
	}
	if err := os.MkdirAll(cgroupDir(parent), 0o755); err != nil {
		return err
	}
	return enableControllers(cgroupDir(parent), "memory", "cpu")
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
