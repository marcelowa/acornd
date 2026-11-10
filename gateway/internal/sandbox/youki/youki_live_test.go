package youki

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/supervise"

	"github.com/marcelowa/acornd/gateway/internal/sandbox"
)

// liveYouki is the real runtime and a real image, for the tests that boot a container: the
// youki tier's gate, the cgroup the budget needs, and the driver installed and usable. Each
// test gets its own data folder and its own workspace; the cgroup parent is shared and removed
// when the last test in this process ends.
func liveYouki(t *testing.T) (*Youki, context.Context) {
	t.Helper()
	if os.Getenv("ACORND_YOUKI_ONLY") == "" {
		t.Skip("the youki tier runs this: it fetches the runtime and an image")
	}
	if os.Getenv("ACORND_IMAGE_TEST") == "" {
		t.Skip("set ACORND_IMAGE_TEST=1 to sandbox.Fetch an image")
	}
	if runtime.GOOS != "linux" {
		t.Skip("youki is a Linux runtime")
	}
	// The cgroup is where a create fails first on a machine that has not delegated one. A
	// plain /sys/fs/cgroup is not a cgroup: the bwrap workspace this suite can run in has no
	// /sys mount at all, and a directory the test makes there is not what youki looks for.
	if _, err := os.Stat(filepath.Join(cgroupMount, "cgroup.controllers")); err != nil {
		t.Skipf("this machine has no cgroup v2 mount, so youki cannot create its cgroup: %v", err)
	}
	parent := cgroupParentPath()
	if err := os.MkdirAll(cgroupDir(parent), 0o755); err != nil {
		t.Skipf("this machine does not delegate a cgroup to the sandbox, so youki cannot create its cgroup: %v", err)
	}
	// The parent slice stays: a rootless run cannot recreate it at the cgroup root, and it is
	// the delegation the next test in this process uses. The driver removes each sandbox's
	// child cgroup when it stops.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	driver := NewYouki(t.TempDir())
	if err := driver.Install(ctx, t.Logf); err != nil {
		t.Fatalf("install the pinned runtime: %v", err)
	}
	if err := driver.Usable(); err != nil {
		t.Fatalf("this machine cannot run a youki sandbox: %v", err)
	}
	return driver, ctx
}

// Live: create/start bring the supervisor up, the socket answers a command, stop keeps the
// files and start brings the same workspace back, and remove takes them — the parts the fake
// runner cannot. The e2e row in internal/e2e is the same engine through the worker and the
// gateway.
func TestYoukiLifecycleAgainstTheRealRuntime(t *testing.T) {
	driver, ctx := liveYouki(t)
	workspace := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	name := sandbox.Name(workspace)
	t.Cleanup(func() { _ = driver.Remove(context.Background(), name) })

	if err := driver.Create(ctx, sandbox.Policy{Workspace: workspace, Image: "debian:stable-slim"}, t.TempDir()); err != nil {
		t.Fatalf("create: %v\nlog: %s", err, driver.logTail(driver.logPath(name)))
	}
	config, err := driver.readConfig(name)
	if err != nil {
		t.Fatalf("the record: %v", err)
	}
	if got, code := runInSupervisor(t, config.Socket, "printf youki-socket-ok"); code != 0 || got != "youki-socket-ok" {
		t.Fatalf("the supervisor should run a command in the sandbox, got %q (exit %d)", got, code)
	}

	// Stop keeps every file; Start brings the same workspace back, and the new supervisor
	// answers on the same socket.
	if err := driver.Stop(ctx, name); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(driver.workspaceDir(name), "rootfs")); err != nil {
		t.Fatalf("stop took the workspace's rootfs: %v", err)
	}
	if err := driver.Start(ctx, name, nil); err != nil {
		t.Fatalf("start after stop: %v\nlog: %s", err, driver.logTail(driver.logPath(name)))
	}
	if got, code := runInSupervisor(t, config.Socket, "printf youki-restarted-ok"); code != 0 || got != "youki-restarted-ok" {
		t.Fatalf("the restarted supervisor should run a command, got %q (exit %d)", got, code)
	}

	if err := driver.Remove(ctx, name); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(driver.configPath(name)); !os.IsNotExist(err) {
		t.Fatalf("remove left the record behind: %v", err)
	}
}

// runInSupervisor is one command through the socket, the way the worker sends it: a JSON line
// out, events back, the chunks until the exit. The exit code is -1 when a signal ended the
// process, which is what a memory kill looks like from Go.
func runInSupervisor(t *testing.T, socket, command string) (string, int) {
	t.Helper()
	connection, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		t.Fatalf("connect to the supervisor at %s: %v", socket, err)
	}
	defer func() { _ = connection.Close() }()
	request := supervise.Request{
		Type: "run",
		ID:   "1",
		Argv: []string{"/bin/sh", "-c", command},
		Env:  map[string]string{"PATH": sandbox.ProcessPath},
		Dir:  "/",
	}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		t.Fatalf("send the command: %v", err)
	}
	decoder := json.NewDecoder(connection)
	var output strings.Builder
	for {
		var event supervise.Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatalf("read the supervisor: %v", err)
		}
		if event.ID != "1" {
			continue
		}
		switch event.Type {
		case "chunk":
			data, err := base64.StdEncoding.DecodeString(event.Data)
			if err != nil {
				t.Fatalf("the supervisor sent a chunk that is not base64: %v", err)
			}
			output.Write(data)
		case "error":
			t.Fatalf("the supervisor could not run the command: %s", event.Error)
		case "exit":
			return output.String(), event.Code
		}
	}
}

// The overlay root: the OCI mounts bind the base, upper and work under /run/acornd-overlay and
// mount the merge at /, so the container's root is the shared image plus this workspace's delta.
// A write lands in the upper and the base is untouched; the mount table says it is an overlay.
func TestYoukiOverlayRootOnThisMachine(t *testing.T) {
	driver, ctx := liveYouki(t)
	if !driver.overlayEnabled() {
		t.Skip("this machine cannot mount an overlay in a private mount namespace; the copy is its shape")
	}
	workspace := filepath.Join(t.TempDir(), "overlay")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	name := sandbox.Name(workspace)
	t.Cleanup(func() {
		if err := driver.Remove(context.Background(), name); err != nil {
			t.Logf("remove: %v", err)
		}
	})
	if err := driver.Create(ctx, sandbox.Policy{Workspace: workspace, Image: "debian:stable-slim"}, t.TempDir()); err != nil {
		t.Fatalf("create: %v\nlog: %s", err, driver.logTail(driver.logPath(name)))
	}
	config, err := driver.readConfig(name)
	if err != nil {
		t.Fatal(err)
	}
	if config.RootMode != "overlay" || config.Base == "" || config.Upper == "" || config.Work == "" {
		t.Fatalf("the record is not an overlay: %+v", config)
	}
	out, code := runInSupervisor(t, config.Socket,
		`printf overlay-ok; touch /made-in-overlay; grep ' / overlay ' /proc/mounts | head -1`)
	if code != 0 || !strings.Contains(out, "overlay-ok") {
		t.Fatalf("the overlay command did not run: %q (exit %d)", out, code)
	}
	if !strings.Contains(out, "overlay") {
		t.Errorf("the sandbox's root is not an overlay mount: %q", out)
	}
	if _, err := os.Stat(filepath.Join(config.Upper, "made-in-overlay")); err != nil {
		t.Errorf("the write did not land in the upper: %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.Base, "made-in-overlay")); !os.IsNotExist(err) {
		t.Errorf("the shared base was written: %v", err)
	}
}
