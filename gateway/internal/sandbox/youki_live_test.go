package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Live: the real runtime and a real image. Step 2's first process is `youki --version`, so the
// container exits at once; what this decides is create, start, state, delete and the mounts,
// which the fake runner cannot. Step 3 replaces the command with the supervisor, and the e2e
// rows in issue #47 §11 take over the parts a command inside the sandbox can see.
func TestYoukiLifecycleAgainstTheRealRuntime(t *testing.T) {
	if os.Getenv("ACORND_YOUKI_ONLY") == "" {
		t.Skip("the youki tier runs this: it fetches the runtime and an image")
	}
	if os.Getenv("ACORND_IMAGE_TEST") == "" {
		t.Skip("set ACORND_IMAGE_TEST=1 to fetch an image")
	}
	if runtime.GOOS != "linux" {
		t.Skip("youki is a Linux runtime")
	}
	// The cgroup is where a create fails first on a machine that has not delegated one. A
	// plain /sys/fs/cgroup is not a cgroup: the bwrap workspace this suite can run in has no
	// /sys mount at all, and a directory the test makes there is not what youki looks for.
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Skipf("this machine has no cgroup v2 mount, so youki cannot create its cgroup: %v", err)
	}
	cgroupRoot := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(youkiCgroupRoot, "/"))
	if err := os.MkdirAll(cgroupRoot, 0o755); err != nil {
		t.Skipf("this machine does not delegate a cgroup to the sandbox, so youki cannot create its cgroup: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(cgroupRoot) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dataDir := t.TempDir()
	driver := NewYouki(dataDir)
	if err := driver.Install(ctx, t.Logf); err != nil {
		t.Fatalf("install the pinned runtime: %v", err)
	}
	if err := driver.Usable(); err != nil {
		t.Skipf("this machine cannot make a user namespace: %v", err)
	}
	workspace := filepath.Join(dataDir, "work")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	name := Name(workspace)
	t.Cleanup(func() { _ = driver.Remove(context.Background(), name) })

	if err := driver.Create(ctx, Policy{Workspace: workspace, Image: "debian:stable-slim"}, filepath.Join(dataDir, "policy")); err != nil {
		t.Fatalf("create: %v\nlog: %s", err, driver.logTail(driver.logPath(name)))
	}
	waitFor(t, 20*time.Second, func() bool {
		return logContains(driver.logPath(name), "youki version:")
	}, "the container's version command did not write its output")

	// Stop keeps every file; Start recreates the container from the recorded spec and the
	// second one writes a second version line.
	if err := driver.Stop(ctx, name); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(driver.workspaceDir(name), "rootfs")); err != nil {
		t.Fatalf("stop took the workspace's rootfs: %v", err)
	}
	if err := driver.Start(ctx, name, nil); err != nil {
		t.Fatalf("start after stop: %v\nlog: %s", err, driver.logTail(driver.logPath(name)))
	}
	waitFor(t, 20*time.Second, func() bool {
		return strings.Count(logText(driver.logPath(name)), "youki version:") >= 2
	}, "the second container did not run")

	if err := driver.Remove(ctx, name); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(driver.configPath(name)); !os.IsNotExist(err) {
		t.Fatalf("remove left the record behind: %v", err)
	}
}

// waitFor waits for a condition with a deadline, never for a fixed duration.
func waitFor(t *testing.T, wait time.Duration, condition func() bool, why string) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s after %s", why, wait)
}

func logContains(path, want string) bool {
	return strings.Contains(logText(path), want)
}

func logText(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}
