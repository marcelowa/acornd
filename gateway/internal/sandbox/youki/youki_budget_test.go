package youki

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/sandbox"
)

// The test that justifies the release: the budget in the record is the cgroup's memory.max and
// cpu.max, a 100 MB allocation fits, a 400 MB one is killed, and Stop removes the cgroup. The
// kernel's own OOM counter is read as well as the exit code, because a fast allocation can be
// killed before it writes anything (issue #47 R2).
func TestYoukiBudgetsKillTheRunaway(t *testing.T) {
	driver, ctx := liveYouki(t)
	workspace := filepath.Join(t.TempDir(), "small")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	name := sandbox.Name(workspace)
	t.Cleanup(func() { _ = driver.Remove(context.Background(), name) })
	policy := sandbox.Policy{Workspace: workspace, Image: "debian:stable-slim", CPUs: 1, Memory: "256M"}
	if err := driver.Create(ctx, policy, t.TempDir()); err != nil {
		t.Fatalf("create: %v\nlog: %s", err, driver.logTail(driver.logPath(name)))
	}
	config, err := driver.readConfig(name)
	if err != nil {
		t.Fatalf("the record: %v", err)
	}
	cgroup := cgroupDir(config.CgroupPath)
	inside := "/sys/fs/cgroup"

	// The policy reaches the cgroup, read from inside the sandbox: a number the runtime wrote,
	// not one this test assumed.
	if got, code := runInSupervisor(t, config.Socket, "cat "+filepath.Join(inside, "memory.max")); code != 0 || strings.TrimSpace(got) != "268435456" {
		t.Fatalf("memory.max inside the sandbox = %q (exit %d), want 268435456", got, code)
	}
	if got, code := runInSupervisor(t, config.Socket, "cat "+filepath.Join(inside, "cpu.max")); code != 0 || strings.TrimSpace(got) != "100000 100000" {
		t.Fatalf("cpu.max inside the sandbox = %q (exit %d), want 100000 100000", got, code)
	}
	if got, code := runInSupervisor(t, config.Socket, "cat "+filepath.Join(inside, "memory.swap.max")); code != 0 || strings.TrimSpace(got) != "0" {
		t.Fatalf("memory.swap.max inside the sandbox = %q (exit %d), want 0: a budget that can swap is not a budget", got, code)
	}

	// A 100 MB allocation fits.
	if got, code := runInSupervisor(t, config.Socket, `perl -e '$x = "x" x (100*1024*1024); print length($x)'`); code != 0 || got != "104857600" {
		t.Fatalf("a 100 MB allocation should complete, got %q (exit %d)", got, code)
	}
	// A 400 MB one does not: the kernel kills it, and the cgroup records the OOM.
	if _, code := runInSupervisor(t, config.Socket, `perl -e '$x = "x" x (400*1024*1024); print length($x)'`); code == 0 {
		t.Fatal("a 400 MB allocation in a 256 M workspace should not complete")
	}
	events, err := os.ReadFile(filepath.Join(cgroup, "memory.events"))
	if err != nil {
		t.Fatalf("memory.events: %v", err)
	}
	if oomKills(events) == 0 {
		t.Fatalf("the kernel did not record an OOM kill: %s", events)
	}

	// Stop ends the container and removes the cgroup; the files stay.
	if err := driver.Stop(ctx, name); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := os.Stat(cgroup); !os.IsNotExist(err) {
		t.Fatalf("stop left the cgroup behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(driver.workspaceDir(name), "rootfs")); err != nil {
		t.Fatalf("stop took the workspace's rootfs: %v", err)
	}
}

// oomKills is the oom_kill counter from a cgroup v2 memory.events file.
func oomKills(events []byte) int {
	for _, line := range strings.Split(string(events), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "oom_kill" {
			count, _ := strconv.Atoi(fields[1])
			return count
		}
	}
	return 0
}

// A cgroup with memory.max but no memory.swap.max is a host that does not account swap, or a
// leaf the kernel will not give the belt. The budget that has to bind is memory.max; a running
// sandbox must not be torn down because the belt is absent.
func TestYoukiKeepsASandboxWithoutTheSwapBelt(t *testing.T) {
	oldMount := cgroupMount
	cgroupMount = t.TempDir()
	t.Cleanup(func() { cgroupMount = oldMount })
	leaf := filepath.Join(cgroupMount, "acornd.slice", "sandbox")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leaf, "memory.max"), []byte("268435456"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A dangling link makes the write fail with ENOENT while memory.max is still there, which
	// is the shape a cgroup without swap accounting has.
	if err := os.Symlink(filepath.Join(leaf, "missing-dir", "missing"), filepath.Join(leaf, "memory.swap.max")); err != nil {
		t.Fatal(err)
	}
	driver := NewYouki(t.TempDir())
	if err := driver.enforceBudget("/acornd.slice/sandbox"); err != nil {
		t.Fatalf("a missing swap belt must not fail the sandbox: %v", err)
	}
}

// Without memory.max nothing bounds the sandbox, so the refusal has to stay.
func TestYoukiRefusesABudgetWithNoMemoryController(t *testing.T) {
	oldMount := cgroupMount
	cgroupMount = t.TempDir()
	t.Cleanup(func() { cgroupMount = oldMount })
	leaf := filepath.Join(cgroupMount, "acornd.slice", "sandbox")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(leaf, "missing-dir", "missing"), filepath.Join(leaf, "memory.swap.max")); err != nil {
		t.Fatal(err)
	}
	driver := NewYouki(t.TempDir())
	err := driver.enforceBudget("/acornd.slice/sandbox")
	if err == nil || !strings.Contains(err.Error(), "memory.max") {
		t.Fatalf("no memory controller must be an error that names it: %v", err)
	}
}

// The parent's controller distribution can be lost with an idle stop. Preparing the record's
// own parent again before a start is what lets the runtime create a leaf with the controllers.
func TestYoukiRepreparesTheBudgetParentBeforeAStart(t *testing.T) {
	oldMount := cgroupMount
	cgroupMount = t.TempDir()
	t.Cleanup(func() { cgroupMount = oldMount })
	base := filepath.Join(cgroupMount, "app.slice")
	parent := filepath.Join(base, "acornd.slice")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(base, "cgroup.subtree_control"),
		filepath.Join(parent, "cgroup.subtree_control"),
	} {
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepareCgroup("/app.slice/acornd.slice"); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for _, path := range []string{
		filepath.Join(base, "cgroup.subtree_control"),
		filepath.Join(parent, "cgroup.subtree_control"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "memory") || !strings.Contains(string(raw), "cpu") {
			t.Errorf("%s = %q, want memory and cpu", path, raw)
		}
	}
}
