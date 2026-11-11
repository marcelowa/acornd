package microsandbox

import (
	"context"
	"github.com/marcelowa/acornd/gateway/internal/sandbox"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	manager, err := sandbox.Open(context.Background(), sandbox.Options{DataDir: dataDir, Driver: liveDriver(dataDir), Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if problem := manager.Problem(); problem != nil {
		t.Fatalf("no sandbox on this machine: %v", problem)
	}
	name, err := manager.Produce(context.Background(), sandbox.Policy{Workspace: ws, Image: "debian:stable-slim", Egress: true}, filepath.Join(dataDir, "state"))
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
	manager, err := sandbox.Open(ctx, sandbox.Options{DataDir: dataDir, Driver: driver, Log: t.Logf})
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
			policy := sandbox.Policy{Workspace: workspace, Image: "debian:stable-slim", DisableVMNesting: test.disable}
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
func TestAMicroVMStartsInsideABwrapSandbox(t *testing.T) {
	if testing.Short() {
		t.Skip("the microVM tier")
	}
	if os.Getenv("ACORND_LIVE_SANDBOX") == "" {
		t.Skip("set ACORND_LIVE_SANDBOX=1 to boot a microVM inside a bwrap sandbox")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("no bubblewrap on this machine")
	}
	// A hypervisor this user cannot open is not a skip: the engine answers it with an abort, and
	// the report is then a crash inside the engine rather than the device that was missing.
	device, err := os.Open("/dev/kvm")
	if err != nil {
		t.Fatalf("the nested microVM needs /dev/kvm, and this machine will not open it: %v%s", err, kvmHint())
	}
	_ = device.Close()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder for the engine store")
	}
	engine := NewMicrosandbox(filepath.Join(home, ".acornd"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if !engine.Installed() {
		// The sandbox tier's first suite can install it; this one must not depend on that
		// ordering, and a skip here would be a green run that booted no microVM.
		if err := engine.Install(ctx, t.Logf); err != nil {
			t.Fatalf("install the microsandbox engine: %v", err)
		}
	}
	store := engine.StateDir()
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatalf("make the engine store: %v", err)
	}
	// Where the host's install and the host's store appear inside the sandbox. The root there
	// is bubblewrap's, so where the host's install appears is this test's to choose.
	const nestedEngine = "/engine"
	args := []string{
		// The sandbox's own root, not the host's. Inside a user namespace that maps one
		// account, the host's / belongs to nobody this process can name, and the pinned engine
		// refuses an engine store when an ancestor could be substituted by another account —
		// the refusal reads "catalog ancestor / permits path substitution". A root bubblewrap
		// makes is the fix, and it is also the shape the product's own bwrap engine has: a
		// rootfs, with what the engine needs from the host bound into it read-only.
		"--tmpfs", "/",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind-try", "/sbin", "/sbin",
		"--ro-bind", "/lib", "/lib",
		// aarch64 Linux has no /lib64, and a missing source is an error rather than a no-op.
		"--ro-bind-try", "/lib64", "/lib64",
		"--ro-bind", "/etc", "/etc",
		// The runners' /etc/resolv.conf is a link into /run, and a root without /run has a
		// resolver that points at nothing: the first pull then fails with "error sending
		// request", which reads like a network outage rather than a missing file.
		"--ro-bind-try", "/run", "/run",
		// The host's install and the host's store. The store keeps its own path, and that is not
		// tidiness: the engine records absolute paths inside it, so a store bound somewhere else
		// inside the sandbox leaves entries the host cannot open afterwards. Bubblewrap creates
		// the parents inside its root, so the ancestry the engine checks is this account's.
		"--ro-bind", engine.InstallDir(), nestedEngine,
		"--bind", store, store,
		"--dev", "/dev",
		"--dev-bind", "/dev/net/tun", "/dev/net/tun",
		"--dev-bind", "/dev/kvm", "/dev/kvm",
		"--cap-add", "ALL",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--unshare-all", "--share-net",
		"--uid", "0", "--gid", "0",
		"--", "env", "MSB_HOME=" + store, filepath.Join(nestedEngine, "bin", "msb"),
		"run", "--net", "public",
		"debian:stable-slim", "--", "/bin/echo", "nested-msb-ok",
	}
	output, err := exec.CommandContext(ctx, "bwrap", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("the microVM did not boot inside the bwrap sandbox: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "nested-msb-ok") {
		t.Fatalf("the microVM ran but its output is missing: %s", output)
	}
}
