package sandbox

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
)

// Live: the real runtime and a real image. The sandbox's first process is the supervisor, so
// what this decides is that create/start bring it up, that the socket answers a command, that
// stop keeps the files and start brings the same workspace back, and that remove takes them —
// the parts the fake runner cannot. The e2e row in internal/e2e is the same engine through the
// worker and the gateway.
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
	config, err := driver.readConfig(name)
	if err != nil {
		t.Fatalf("the record: %v", err)
	}
	if got := runInSupervisor(t, config.Socket, "printf youki-socket-ok"); got != "youki-socket-ok" {
		t.Fatalf("the supervisor should run a command in the sandbox, got %q", got)
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
	if got := runInSupervisor(t, config.Socket, "printf youki-restarted-ok"); got != "youki-restarted-ok" {
		t.Fatalf("the restarted supervisor should run a command, got %q", got)
	}

	if err := driver.Remove(ctx, name); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(driver.configPath(name)); !os.IsNotExist(err) {
		t.Fatalf("remove left the record behind: %v", err)
	}
}

// runInSupervisor is one command through the socket, the way the worker sends it: a JSON line
// out, events back, the chunks until the exit.
func runInSupervisor(t *testing.T, socket, command string) string {
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
		Env:  map[string]string{"PATH": processPath},
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
			return output.String()
		}
	}
}
