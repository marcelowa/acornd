package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBwrap answers the two calls the driver makes — the version and the probe — so the suite
// runs the same on a machine with bubblewrap and one without.
type fakeBwrap struct {
	version string
	probe   error
	calls   [][]string
}

func (f *fakeBwrap) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if len(args) == 1 && args[0] == "--version" {
		return f.version, nil
	}
	if f.probe != nil {
		return "bwrap: setting up uid map: Permission denied", f.probe
	}
	return "", nil
}

func testBwrap(t *testing.T, home string) (*Bwrap, *fakeBwrap) {
	t.Helper()
	runner := &fakeBwrap{version: "bubblewrap 0.11.0"}
	driver := NewBwrap(home).
		WithRunner(runner).
		WithImageSource(fakeImage).
		WithLookPath(func(string) (string, error) { return "/usr/bin/bwrap", nil })
	return driver, runner
}

// fakeImage is a registry that answers with a two-file root, so the pull is not this suite's
// business (image_test.go covers it) and no test needs a network.
func fakeImage(_ context.Context, reference, dest string, _ func(string, ...any)) error {
	for _, dir := range []string{filepath.Join(dest, "etc"), filepath.Join(dest, "usr", "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dest, "etc", "os-release"), []byte("ID=test\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dest, "usr", "bin", "sh"), []byte("#!/bin/sh\n"), 0o755)
}

func TestBwrapVersionIsBubblewrapsOwn(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	if got := driver.Version(); got != "0.11.0" {
		t.Fatalf("version: %q, want 0.11.0", got)
	}
	// Read once: the version is a property of the binary, not of every state frame.
	if got := driver.Version(); got != "0.11.0" {
		t.Fatalf("second read: %q", got)
	}
}

func TestBwrapRefusesWhenTheMachineWillNotMakeAUserNamespace(t *testing.T) {
	home := t.TempDir()
	driver, runner := testBwrap(t, home)
	runner.probe = errors.New("exit status 1")
	err := driver.Usable()
	if err == nil {
		t.Fatal("Usable accepted a machine that refuses a user namespace")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error is not a refusal: %v", err)
	}
	// The sentence names the reason and the fix, because "sandbox unavailable" is not one.
	for _, want := range []string{"user namespace", "AppArmor", "Ubuntu 24.04"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestBwrapInstallNamesTheCommand(t *testing.T) {
	driver := NewBwrap(t.TempDir()).
		WithRunner(&fakeBwrap{}).
		WithLookPath(func(string) (string, error) { return "", errors.New("not found") })
	if driver.Installed() {
		t.Fatal("Installed said yes with no binary")
	}
	err := driver.Install(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "apt install bubblewrap") {
		t.Fatalf("install error: %v, want the command that fixes it", err)
	}
}

func TestBwrapCreateWritesWhatTheWorkspaceRunsUnder(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(filepath.Join(home, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	driver, _ := testBwrap(t, home)
	policy := Policy{
		Workspace: workspace,
		Image:     "debian:stable-slim",
		Egress:    false,
		Global:    []string{filepath.Join(home, "skills"), filepath.Join(home, "absent")},
	}
	if err := driver.Create(context.Background(), policy, t.TempDir()); err != nil {
		t.Fatalf("create: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(driver.StateDir(), Name(workspace)+".json"))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var config bwrapConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("config is not json: %v", err)
	}
	if config.Workspace != workspace || config.Engine != "bwrap" {
		t.Fatalf("config names %q on engine %q", config.Workspace, config.Engine)
	}
	if config.Network.Mode != NetworkOffName {
		t.Fatalf("network: %q, want off", config.Network.Mode)
	}
	// Only folders that exist are mounted: a mount that does not resolve would make every
	// command fail, and a later-created folder is the next command's business.
	if len(config.ReadOnly) != 1 || config.ReadOnly[0] != filepath.Join(home, "skills") {
		t.Fatalf("read-only mounts: %v", config.ReadOnly)
	}
	// The private home is inside the engine's folder, so removing the sandbox removes it.
	if !strings.HasPrefix(config.Home, driver.StateDir()) {
		t.Fatalf("home %q is not inside %q", config.Home, driver.StateDir())
	}
	// The workspace's own copy of its image, and the apt line that makes a container root
	// usable under a single-mapping user namespace.
	if _, err := os.Stat(filepath.Join(config.Rootfs, "etc", "os-release")); err != nil {
		t.Fatalf("the rootfs was not copied from the image: %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.Rootfs, "etc", "apt", "apt.conf.d", "99-acornfall")); err != nil {
		t.Fatalf("the apt sandbox line was not written: %v", err)
	}
	info, err := os.Stat(config.Home)
	if err != nil || !info.IsDir() {
		t.Fatalf("home was not created: %v", err)
	}
}

// Egress is carried by pasta: the sandbox gets an address inside a namespace of its own, and the
// host's own stack is not what a command is on.
func TestBwrapCreateCarriesEgressWithPasta(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	driver, _ := testBwrap(t, home)
	driver = driver.WithLookPath(func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	})
	if err := driver.Create(context.Background(), Policy{Workspace: workspace, Egress: true}, t.TempDir()); err != nil {
		t.Fatalf("create: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(driver.StateDir(), Name(workspace)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var config bwrapConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if config.Network.Mode != "pasta" {
		t.Fatalf("mode: %q, want pasta", config.Network.Mode)
	}
	// The tools are resolved now, so a missing one is a sentence at create rather than a mystery
	// in the middle of a turn.
	if config.Network.Pasta != "/usr/bin/pasta" || config.Network.IP != "/usr/bin/ip" {
		t.Fatalf("tool paths: %+v", config.Network)
	}
	if config.Network.Address != sandboxAddress || config.Network.Gateway != sandboxGateway {
		t.Fatalf("address: %+v", config.Network)
	}
}

// A machine without the two packages cannot carry egress, and saying which one is missing is the
// difference between a refusal somebody can act on and one they cannot.
func TestBwrapCreateNamesWhatEgressNeeds(t *testing.T) {
	workspace := t.TempDir()
	withOnly := func(available string) *Bwrap {
		return NewBwrap(t.TempDir()).
			WithRunner(&fakeBwrap{version: "bubblewrap 0.11.0"}).
			WithImageSource(fakeImage).
			WithLookPath(func(name string) (string, error) {
				if name == available || name == "bwrap" {
					return "/usr/bin/" + name, nil
				}
				return "", errors.New("not found")
			})
	}
	err := withOnly("bwrap").Create(context.Background(), Policy{Workspace: workspace, Egress: true}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "apt install passt") {
		t.Fatalf("no pasta: %v, want the package that provides it", err)
	}
	err = withOnly("pasta").Create(context.Background(), Policy{Workspace: workspace, Egress: true}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "iproute2") {
		t.Fatalf("no ip: %v, want the package that provides it", err)
	}
	// Without egress neither is needed: a sandbox with no network needs no network tools.
	if err := withOnly("bwrap").Create(context.Background(), Policy{Workspace: workspace}, t.TempDir()); err != nil {
		t.Fatalf("network off should not need pasta: %v", err)
	}
}

func TestBwrapCreateIsIdempotent(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	driver, _ := testBwrap(t, home)
	policy := Policy{Workspace: workspace}
	for i := 0; i < 2; i++ {
		if err := driver.Create(context.Background(), policy, t.TempDir()); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	// Twice is the same state, which is what makes a changed policy apply to the next
	// command rather than to a stored copy nobody updates.
	if _, err := os.Stat(filepath.Join(driver.StateDir(), Name(workspace)+".json")); err != nil {
		t.Fatalf("config after two creates: %v", err)
	}
}

func TestBwrapRemoveDeletesTheHomeAndNotTheWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	driver, _ := testBwrap(t, home)
	if err := driver.Create(context.Background(), Policy{Workspace: workspace}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(driver.StateDir(), Name(workspace)+".json")
	if err := driver.Remove(context.Background(), Name(workspace)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config survived remove: %v", err)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("remove took the workspace with it: %v", err)
	}
	if _, err := os.Stat(filepath.Join(driver.StateDir(), Name(workspace))); !os.IsNotExist(err) {
		t.Fatalf("remove left the sandbox's own folder behind: %v", err)
	}
	// Removing what is not there is already the wanted state.
	if err := driver.Remove(context.Background(), "acornfall-absent"); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

func TestBwrapHasNoSandboxObject(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	instances, err := driver.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(instances) != 0 {
		t.Fatalf("listed %d sandboxes; bwrap has no objects to list", len(instances))
	}
	// Start and Stop are no-ops that must not fail: a workspace whose engine boots nothing
	// still provisions on every worker start.
	if err := driver.Start(context.Background(), "x", nil); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := driver.Stop(context.Background(), "x"); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestBwrapDeliversSecretsInTheEnvironment(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	if driver.SecretDelivery() != DeliveryEnvironment {
		t.Fatalf("delivery: %v, want environment", driver.SecretDelivery())
	}
	// The values belong in the worker's process, which is what a command inherits; there is
	// no guest to substitute into.
	if !driver.SecretEnvInWorker() {
		t.Fatal("secrets are not delivered to the worker")
	}
	change, err := driver.SecretsChanged(context.Background(), "x", nil)
	if err != nil || !change.RestartRequired {
		t.Fatalf("change: %+v, %v; want a restart", change, err)
	}
}

func TestBwrapDoesNotClaimNesting(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	support := driver.Nesting(context.Background())
	if support.Available {
		t.Fatal("bwrap claims a hypervisor can start inside it; the config binds no /dev/kvm")
	}
	if !strings.Contains(support.Reason, "/dev/kvm") {
		t.Fatalf("reason %q does not say what is missing", support.Reason)
	}
}

func TestBwrapIsARealEngineOfTheSeam(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	if !driver.Isolated() {
		t.Fatal("bwrap reported unisolated")
	}
	if driver.Name() != "bwrap" {
		t.Fatalf("name %q", driver.Name())
	}
	if !strings.Contains(strings.Join(driver.Env(), " "), "ACORNFALL_BWRAP_STATE="+driver.StateDir()) {
		t.Fatalf("env does not point the worker at the configs: %v", driver.Env())
	}
	// The seam's own list has to offer it, or a workspace cannot choose it.
	if !ValidEngine("bwrap") {
		t.Fatal("bwrap is not a named engine")
	}
	if _, err := Engine(t.TempDir(), "", "bwrap"); err != nil {
		t.Fatalf("Engine(bwrap): %v", err)
	}
}

// The real binary, when this machine has one. It is a sliver of the worker's suite: it proves
// the probe and the version read a real bubblewrap, which the fake cannot.
func TestBwrapRealBinary(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("no bubblewrap on this machine")
	}
	driver := NewBwrap(t.TempDir())
	if version := driver.Version(); version == "" {
		t.Fatal("no version from a real binary")
	}
	if err := driver.Usable(); err != nil {
		t.Skipf("this machine will not make a user namespace: %v", err)
	}
}
