package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
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
	name := Name(workspace)
	t.Cleanup(func() { _ = driver.Remove(context.Background(), name) })
	policy := Policy{Workspace: workspace, Image: "debian:stable-slim", CPUs: 1, Memory: "256M"}
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
