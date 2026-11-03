package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// liveDriver is the real engine, with its state directory shared when the environment names
// one. A fresh directory per run means a fresh pull of the image every run, which is how a
// suite meets a registry's anonymous rate limit; the shared directory is also what the e2e
// suite's image cache is.
func liveDriver(dataDir string) *Microsandbox {
	driver := NewMicrosandbox(dataDir)
	if state := os.Getenv("ACORND_SANDBOX_STATE"); state != "" {
		driver = driver.WithStateDir(state)
	}
	return driver
}

// Live: only where a hypervisor exists. Proves the CLI shapes against the real engine.
func TestLiveProduceAndStop(t *testing.T) {
	if os.Getenv("ACORND_LIVE_SANDBOX") == "" {
		t.Skip("set ACORND_LIVE_SANDBOX=1 to boot a real sandbox")
	}
	dataDir := t.TempDir()
	ws := filepath.Join(dataDir, "work")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("from the host\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := Open(context.Background(), Options{DataDir: dataDir, Driver: liveDriver(dataDir), Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if problem := manager.Problem(); problem != nil {
		t.Fatalf("no sandbox on this machine: %v", problem)
	}
	name, err := manager.Produce(context.Background(), Policy{Workspace: ws, Image: "debian:stable-slim", Egress: true}, filepath.Join(dataDir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sandbox %s", name)
	status, err := manager.Status(context.Background(), ws)
	if err != nil || status == "" {
		t.Fatalf("status %q err %v", status, err)
	}
	if err := manager.Stop(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
}

// Live: nesting reaches the guest, in both directions. The switch is a flag the driver
// renders, so the guest is where it can be seen: /dev/kvm inside the sandbox is what
// `--nested-virt` buys, and its absence is what the explicit false keeps. That the create
// succeeds at all is the other half — an argument the engine does not know fails it — so a
// red test here also means the rendering and the pinned release disagree.
//
// A workspace that allows nesting gets the machine's answer, asserted either way: a host
// that can nest must show the device, and one that cannot must not, which is the case that
// would otherwise cost somebody their workspace, since an enabled setting fails the boot
// there. A workspace that refuses nesting gets a guest without a hypervisor on any host.
func TestALiveSandboxNestsOnlyWhenTheWorkspaceAllowsIt(t *testing.T) {
	if os.Getenv("ACORND_LIVE_SANDBOX") == "" {
		t.Skip("set ACORND_LIVE_SANDBOX=1 to boot a real sandbox")
	}
	ctx := context.Background()
	dataDir := t.TempDir()
	driver := liveDriver(dataDir)
	manager, err := Open(ctx, Options{DataDir: dataDir, Driver: driver, Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if problem := manager.Problem(); problem != nil {
		t.Fatalf("no sandbox on this machine: %v", problem)
	}
	for _, test := range []struct {
		name    string
		disable bool
	}{
		{"the workspace allows nesting", false},
		{"the workspace refuses nesting", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := filepath.Join(dataDir, strings.ReplaceAll(test.name, " ", "-"))
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatal(err)
			}
			policy := Policy{Workspace: workspace, Image: "debian:stable-slim", DisableVMNesting: test.disable}
			name, err := manager.Produce(ctx, policy, filepath.Join(dataDir, "state"))
			if err != nil {
				t.Fatalf("the create call did not take the nesting argument: %v", err)
			}
			// The sandbox is this test's own: the tier shares its image cache, not its sandboxes.
			t.Cleanup(func() {
				if err := manager.Remove(ctx, workspace); err != nil {
					t.Errorf("cannot remove %s: %v", name, err)
				}
			})
			// Asked once the engine exists: until it is installed the answer is the
			// engine's, and control installs it on the way to the create.
			nesting := manager.Nesting(ctx)
			guest, err := driver.run(ctx, "exec", name, "--", "/bin/sh", "-c",
				"if [ -c /dev/kvm ]; then echo nested; else echo flat; fi")
			if err != nil {
				t.Fatalf("cannot ask the guest about /dev/kvm: %v", err)
			}
			got := strings.TrimSpace(guest)
			if test.disable {
				if got != "flat" {
					t.Errorf("a workspace that refuses nesting must not have a hypervisor in its guest, got %q", got)
				}
				return
			}
			if !nesting.Available {
				if got != "flat" {
					t.Errorf("this machine cannot nest (%s), so its guests should not have /dev/kvm, got %q", nesting.Reason, got)
				}
				return
			}
			if got != "nested" {
				t.Errorf("this machine can nest and the workspace allows it, so the guest should have /dev/kvm, got %q", got)
			}
		})
	}
}
