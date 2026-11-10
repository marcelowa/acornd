package youki

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/sandbox"
)

// The parent's cgroup.subtree_control is the delegation: a controller that is already on is
// left alone, and only the missing one is written. A second probe on a machine that is already
// set up should cost a read.
func TestYoukiEnablesOnlyTheMissingControllers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cgroup.subtree_control")
	if err := os.WriteFile(path, []byte("cpu"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := enableControllers(dir, "memory", "cpu"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "+memory" {
		t.Errorf("wrote %q, want only the missing controller", raw)
	}
}

// A parent that is not a cgroup is a delegation the machine does not have, and the probe's
// caller gets the reason rather than a silent budget that does not bind.
func TestYoukiCgroupProbeNamesAMissingSubtreeControl(t *testing.T) {
	if err := enableControllers(t.TempDir(), "memory"); err == nil {
		t.Fatal("a directory without cgroup.subtree_control should be refused")
	}
}

// A rootless systemd user's process lives in a service or a scope; the sibling `acornd.slice`
// under the slice that owns it is the cgroup the user can write. The current cgroup is tried
// second, for a delegated cgroup with no processes, and the root last for a non-systemd machine.
func TestYoukiCgroupCandidatesStartAtTheDelegatedParent(t *testing.T) {
	for _, tc := range []struct {
		current string
		first   string
		last    string
	}{
		{
			current: "/user.slice/user-1000.slice/user@1000.service/app.slice/acornfall.service",
			first:   "/user.slice/user-1000.slice/user@1000.service/app.slice/acornd.slice",
			last:    "/acornd.slice",
		},
		{
			current: "/user.slice/user-1000.slice/user@1000.service/service/init",
			first:   "/user.slice/user-1000.slice/user@1000.service/service/acornd.slice",
			last:    "/acornd.slice",
		},
		{
			current: "/user.slice/user-1000.slice/session-3.scope",
			first:   "/user.slice/user-1000.slice/acornd.slice",
			last:    "/acornd.slice",
		},
		{current: "/", first: "/acornd.slice", last: "/acornd.slice"},
	} {
		candidates := cgroupCandidates(tc.current)
		if len(candidates) == 0 || candidates[0] != tc.first || candidates[len(candidates)-1] != tc.last {
			t.Errorf("candidates(%q) = %v, want first %q and last %q", tc.current, candidates, tc.first, tc.last)
		}
	}
}

func TestYoukiCgroupPathUsesTheProbedParent(t *testing.T) {
	driver := NewYouki(t.TempDir())
	driver.cgroupParent = "/user.slice/user-1000.slice/user@1000.service/app.slice/acornd.slice"
	if got, want := driver.cgroupPathFor("acornd-ws"), "/user.slice/user-1000.slice/user@1000.service/app.slice/acornd.slice/acornd-ws"; got != want {
		t.Fatalf("cgroupPathFor = %q, want %q", got, want)
	}
}

// fakeCgroup makes one cgroup directory in a test's cgroup root, with the subtree control file
// the kernel would create. A missing file is a parent this process cannot delegate through.
func fakeCgroup(t *testing.T, root, cgroupPath, controllers string) {
	t.Helper()
	dir := filepath.Join(root, strings.TrimPrefix(cgroupPath, "/"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte(controllers), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestYoukiProbeChoosesTheDelegatedCgroup(t *testing.T) {
	root := t.TempDir()
	oldMount, oldCurrent := cgroupMount, currentCgroupPath
	cgroupMount = root
	currentCgroupPath = func() string {
		return "/user.slice/user-1000.slice/user@1000.service/app.slice/acornfall.service"
	}
	t.Cleanup(func() { cgroupMount, currentCgroupPath = oldMount, oldCurrent })
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	app := "/user.slice/user-1000.slice/user@1000.service/app.slice"
	fakeCgroup(t, root, app, "cpu memory pids\n")
	fakeCgroup(t, root, app+"/acornd.slice", "")

	y := NewYouki(t.TempDir())
	if err := y.cgroup(); err != nil {
		t.Fatalf("probe the delegated cgroup: %v", err)
	}
	if want := app + "/acornd.slice"; y.cgroupParent != want {
		t.Fatalf("parent = %q, want %q", y.cgroupParent, want)
	}
	if got, want := y.cgroupPathFor("acornd-ws"), app+"/acornd.slice/acornd-ws"; got != want {
		t.Fatalf("cgroupPathFor = %q, want %q", got, want)
	}
}

// The current cgroup is tried second: a delegated cgroup with no processes of its own is a valid
// parent, and the first candidate's slice is not writable here.
func TestYoukiProbeFallsBackToAChildOfItsOwnCgroup(t *testing.T) {
	root := t.TempDir()
	oldMount, oldCurrent := cgroupMount, currentCgroupPath
	cgroupMount = root
	current := "/user.slice/user-1000.slice/session-3.scope"
	currentCgroupPath = func() string { return current }
	t.Cleanup(func() { cgroupMount, currentCgroupPath = oldMount, oldCurrent })
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fakeCgroup(t, root, current, "cpu memory\n")
	fakeCgroup(t, root, current+"/acornd.slice", "")

	y := NewYouki(t.TempDir())
	if err := y.cgroup(); err != nil {
		t.Fatalf("probe the current cgroup: %v", err)
	}
	if want := current + "/acornd.slice"; y.cgroupParent != want {
		t.Fatalf("parent = %q, want %q", y.cgroupParent, want)
	}
}

// A refused probe names every path it tried, so the operator sees the delegation they need rather
// than a sentence about a cgroup that does not say where.
func TestYoukiProbeNamesEveryCandidateItTried(t *testing.T) {
	root := t.TempDir()
	oldMount, oldCurrent := cgroupMount, currentCgroupPath
	cgroupMount = root
	currentCgroupPath = func() string {
		return "/user.slice/user-1000.slice/user@1000.service/app.slice/acornfall.service"
	}
	t.Cleanup(func() { cgroupMount, currentCgroupPath = oldMount, oldCurrent })
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	y := NewYouki(t.TempDir())
	err := y.cgroup()
	if err == nil {
		t.Fatal("a cgroup root with no delegation should be refused")
	}
	for _, want := range []string{
		"/user.slice/user-1000.slice/user@1000.service/app.slice/acornd.slice",
		"/acornd.slice",
		"Delegate=yes",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should name %q: %v", want, err)
		}
	}
}

func TestYoukiProbeWithoutACgroupMount(t *testing.T) {
	oldMount := cgroupMount
	cgroupMount = t.TempDir()
	t.Cleanup(func() { cgroupMount = oldMount })

	y := NewYouki(t.TempDir())
	err := y.cgroup()
	if err == nil || !strings.Contains(err.Error(), "no cgroup v2 mount") {
		t.Fatalf("a machine without cgroup v2 should be refused by name: %v", err)
	}
}

func TestYoukiCurrentCgroupIsRead(t *testing.T) {
	if got := currentCgroup(); got == "" {
		t.Fatal("every Linux process has a cgroup in /proc/self/cgroup")
	}
}

func TestYoukiCgroupPathFallsBackToTheRoot(t *testing.T) {
	var y Youki
	if got, want := y.cgroupPathFor("acornd-ws"), "/acornd.slice/acornd-ws"; got != want {
		t.Fatalf("cgroupPathFor = %q, want %q", got, want)
	}
}

// A direct Create does not go through the manager's Usable probe; Prepare has to ask before the
// record names a parent the runtime cannot write.
func TestYoukiPrepareProbesTheCgroupBeforeTheRecord(t *testing.T) {
	y := NewYouki(t.TempDir())
	y.cgroup = func() error { return errors.New("no delegated cgroup") }
	err := y.Prepare(context.Background(), sandbox.Policy{}, "")
	if err == nil || !strings.Contains(err.Error(), "no delegated cgroup") {
		t.Fatalf("Prepare should refuse before writing a record: %v", err)
	}
}
