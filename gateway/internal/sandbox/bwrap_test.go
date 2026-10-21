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

	"github.com/acornfall/acornfall/gateway/internal/supervise"
)

// TestMain lets this binary be the sandbox's supervisor, which is what a started sandbox runs.
// The driver binds this executable into the sandbox and runs it with the supervisor's arguments;
// a test binary that ignored them would start a second test run inside the sandbox instead.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "sandbox-supervisor" {
		socket := ""
		for index, argument := range os.Args {
			if argument == "--socket" && index+1 < len(os.Args) {
				socket = os.Args[index+1]
			}
		}
		if socket != "" {
			_ = supervise.New(socket, "test", 0).Serve(context.Background())
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

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

// fakeImage is a registry that answers with a minimal root, so the pull is not this suite's
// business (image_test.go covers it) and no test needs a network.
//
// The root carries this binary's own libraries when it has any: the sandbox runs this executable
// as its supervisor, and an image with no libc can only run a static one — which is exactly what a
// release build is, and exactly what a test binary is not.
func fakeImage(_ context.Context, reference, dest string, _ func(string, ...any)) error {
	for _, dir := range []string{filepath.Join(dest, "etc"), filepath.Join(dest, "usr", "bin"), filepath.Join(dest, "usr", "local", "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dest, "etc", "os-release"), []byte("ID=test\n"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dest, "usr", "bin", "sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil
	}
	libraries, err := exec.Command("ldd", executable).Output()
	if err != nil {
		return nil
	}
	for _, field := range strings.Fields(string(libraries)) {
		if !strings.HasPrefix(field, "/") {
			continue
		}
		target := filepath.Join(dest, field)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		content, err := os.ReadFile(field)
		if err != nil {
			continue
		}
		if err := os.WriteFile(target, content, 0o755); err != nil {
			return err
		}
	}
	return nil
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
	if _, err := driver.writeConfig(policy, t.TempDir()); err != nil {
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
	// A policy that names no idle timeout gets the default one: a sandbox nobody told about the
	// timer would keep running after a gateway died.
	if config.IdleTimeout != DefaultIdleTimeout {
		t.Fatalf("idle timeout: %q, want %q", config.IdleTimeout, DefaultIdleTimeout)
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
	if _, err := driver.writeConfig(Policy{Workspace: workspace, Egress: true}, t.TempDir()); err != nil {
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
	if config.Network.Pasta != "/usr/bin/pasta" {
		t.Fatalf("tool paths: %+v", config.Network)
	}
	if config.Network.Address != sandboxAddress || config.Network.Gateway != sandboxGateway {
		t.Fatalf("address: %+v", config.Network)
	}
}

// A machine without pasta cannot carry egress, and saying which package provides it is the
// difference between a refusal somebody can act on and one they cannot. Nothing else is needed:
// the network namespace is configured over netlink by this binary (ADR 0026).
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
	_, err := withOnly("bwrap").writeConfig(Policy{Workspace: workspace, Egress: true}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "apt install passt") {
		t.Fatalf("no pasta: %v, want the package that provides it", err)
	}
	// Egress with pasta and nothing else is a complete network: no iproute2 to install.
	if _, err := withOnly("pasta").writeConfig(Policy{Workspace: workspace, Egress: true}, t.TempDir()); err != nil {
		t.Fatalf("pasta alone should be enough for egress: %v", err)
	}
	// Without egress neither is needed: a sandbox with no network needs no network tools.
	if _, err := withOnly("bwrap").writeConfig(Policy{Workspace: workspace}, t.TempDir()); err != nil {
		t.Fatalf("network off should not need pasta: %v", err)
	}
}

func TestBwrapCreateIsIdempotent(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	driver, _ := testBwrap(t, home)
	policy := Policy{Workspace: workspace}
	for i := 0; i < 2; i++ {
		if _, err := driver.writeConfig(policy, t.TempDir()); err != nil {
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
	if _, err := driver.writeConfig(Policy{Workspace: workspace}, t.TempDir()); err != nil {
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

// The sandbox's argv is one command that holds the workspace's view for as long as the sandbox
// lives: the mounts are decided once, and commands run inside it.
func TestBwrapSandboxArgvIsOneSandboxPerWorkspace(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	config := bwrapConfig{
		Engine:      "bwrap",
		Workspace:   "/work/ws",
		Image:       "debian:stable-slim",
		Rootfs:      "/state/ws/rootfs",
		Home:        "/state/ws/home",
		Hidden:      "/home/me/.acornfall",
		ReadOnly:    []string{"/home/me/.agents"},
		Bwrap:       "/usr/bin/bwrap",
		Supervisor:  "/install/acornfall",
		Run:         "/state/ws/run",
		GuestRun:    guestRunDir,
		GuestSocket: guestSocketPath,
		Network:     bwrapNetwork{Mode: "off"},
		IdleTimeout: DefaultIdleTimeout,
	}
	argv, err := driver.sandboxArgv(config)
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	joined := strings.Join(argv, " ")
	if argv[0] != "/usr/bin/bwrap" {
		t.Fatalf("argv starts with %q", argv[0])
	}
	for _, want := range []string{
		// The workspace's own image as `/`, as root inside a single mapping.
		"--bind /state/ws/rootfs / --uid 0 --gid 0",
		// A root still needs the machine's resolver, read-only.
		"--ro-bind /etc/resolv.conf /etc/resolv.conf",
		// The private home, the workspace at its own path, and the socket's folder on both sides.
		"--bind /state/ws/home /root",
		"--bind /work/ws /work/ws",
		"--bind /state/ws/run /run/acornfall",
		"--ro-bind /home/me/.agents /home/me/.agents",
		// The supervisor is this binary, bound in, and it is what the sandbox runs.
		"--ro-bind /install/acornfall /usr/local/bin/acornfall",
		"-- /usr/local/bin/acornfall sandbox-supervisor --socket /run/acornfall/s.sock",
		// The same policy value the microVM engine is given: both engines answer "stop when idle"
		// the same way, and a kept-alive workspace asks for a timeout that never fires.
		"--idle-timeout 30m",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv should contain %q:\n%s", want, joined)
		}
	}
	// No egress: a namespace with no interfaces, and nothing of the machine's stack.
	if strings.Contains(joined, "--share-net") {
		t.Errorf("network off must not share a network namespace:\n%s", joined)
	}
}

// With egress the sandbox still gets a namespace of its own; pasta is what carries it, so the
// bubblewrap process shares pasta's namespace rather than the machine's.
func TestBwrapSandboxArgvSharesPastaForEgress(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	config := bwrapConfig{
		Workspace:   "/work/ws",
		Rootfs:      "/state/ws/rootfs",
		Home:        "/state/ws/home",
		Bwrap:       "/usr/bin/bwrap",
		Supervisor:  "/install/acornfall",
		Run:         "/state/ws/run",
		GuestRun:    guestRunDir,
		GuestSocket: guestSocketPath,
		Network: bwrapNetwork{
			Mode: "pasta", Address: "10.0.2.15/24", Gateway: "10.0.2.2",
			Pasta: "/usr/bin/pasta",
		},
	}
	argv, err := driver.bootArgv(config)
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	joined := strings.Join(argv, " ")
	if argv[0] != "/usr/bin/pasta" {
		t.Fatalf("the boot argv starts with %q, want pasta", argv[0])
	}
	for _, want := range []string{
		// The machine's own loopback is not the workspace's, by either route pasta offers: the
		// automatic port forwarding mirrors every host listener into the sandbox, and the gateway
		// translation maps 10.0.2.2 onto the same loopback. A sandbox that reached 127.0.0.1:7777
		// would be talking to the acornfall that runs it.
		"-t none -u none -T none -U none", "--no-map-gw",
		"-a 10.0.2.15 -n 24 -g 10.0.2.2",
		// pasta configures neither the address nor the route, so the sandbox does it from inside
		// the namespace pasta made: this binary over netlink, not a shell and not iproute2.
		"/install/acornfall netns --address 10.0.2.15/24 --gateway 10.0.2.2",
		"/usr/bin/bwrap",
		"--share-net",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("boot argv should contain %q:\n%s", want, joined)
		}
	}
}

// A socket path has a length limit, and the host side carries the workspace's name. That is a
// refusal with a sentence, not a kernel error passed through.
func TestBwrapRefusesASocketPathThatIsTooLong(t *testing.T) {
	home := filepath.Join(t.TempDir(), strings.Repeat("very-long-folder-name/", 4))
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	driver, _ := testBwrap(t, home)
	_, err := driver.writeConfig(Policy{Workspace: workspace}, t.TempDir())
	if err == nil {
		t.Skip("this machine's temp folder is short enough that the path fits")
	}
	if !strings.Contains(err.Error(), "too long") || !strings.Contains(err.Error(), "--state") {
		t.Fatalf("the refusal should name the limit and the way out: %v", err)
	}
}

// A created sandbox is a running sandbox: control's policy is "ensure created, then ensure
// running", so Create boots it and the engine reports it. A stopped one is listed as stopped, so
// the next attempt starts it instead of remaking it.
func TestBwrapHasALifecycleLikeEveryOtherEngine(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("no bubblewrap on this machine")
	}
	home := t.TempDir()
	workspace := t.TempDir()
	driver := NewBwrap(home).
		WithImageSource(fakeImage).
		WithLookPath(func(name string) (string, error) {
			if path, err := exec.LookPath(name); err == nil {
				return path, nil
			}
			return "/usr/bin/" + name, nil
		})
	t.Cleanup(func() { _ = driver.Remove(context.Background(), Name(workspace)) })
	if err := driver.Create(context.Background(), Policy{Workspace: workspace}, t.TempDir()); err != nil {
		if strings.Contains(err.Error(), "user namespace") {
			t.Skipf("this machine will not make a user namespace: %v", err)
		}
		t.Fatalf("create: %v", err)
	}
	instances, err := driver.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(instances) != 1 || !instances[0].Running() {
		t.Fatalf("a created sandbox should be listed and running: %+v", instances)
	}
	if err := driver.Stop(context.Background(), Name(workspace)); err != nil {
		t.Fatalf("stop: %v", err)
	}
	instances, _ = driver.List(context.Background())
	if len(instances) != 1 || instances[0].Running() {
		t.Fatalf("a stopped sandbox should be listed and not running: %+v", instances)
	}
	// The files are the point of stopping rather than removing.
	if _, err := os.Stat(filepath.Join(driver.StateDir(), Name(workspace), "rootfs")); err != nil {
		t.Fatalf("stop took the workspace's root with it: %v", err)
	}
	if err := driver.Start(context.Background(), Name(workspace), nil); err != nil {
		t.Fatalf("start after stop: %v", err)
	}
	instances, _ = driver.List(context.Background())
	if len(instances) != 1 || !instances[0].Running() {
		t.Fatalf("a restarted sandbox should be running: %+v", instances)
	}
	if err := driver.Remove(context.Background(), Name(workspace)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if instances, _ := driver.List(context.Background()); len(instances) != 0 {
		t.Fatalf("a removed sandbox is gone: %+v", instances)
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

// A machine that resolves through a loopback address cannot hand that address to a sandbox:
// 127.0.0.1 in there is the sandbox's own loopback. The measured symptom is an `apt-get update`
// that works on the host and fails inside.
func TestBwrapBindsUpstreamResolversWhenTheMachineUsesALoopbackOne(t *testing.T) {
	dir := t.TempDir()
	host := filepath.Join(dir, "host-resolv.conf")
	upstream := filepath.Join(dir, "upstream-resolv.conf")
	if err := os.WriteFile(host, []byte("nameserver 127.0.0.53\nsearch example.test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(upstream, []byte("nameserver 100.100.100.100\nnameserver 1.1.1.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	driver := NewBwrap(t.TempDir())
	driver.resolver = resolverFiles{Host: host, Upstream: upstream}
	workspace := filepath.Join(dir, "state", "acornfall-test")
	got, err := driver.resolverFile(workspace)
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	if got == "" || got == host {
		t.Fatalf("the machine's loopback resolver was bound: %q", got)
	}
	bound, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("the file to bind: %v", err)
	}
	if !strings.Contains(string(bound), "100.100.100.100") {
		t.Fatalf("the bound file should carry the machine's upstreams, got %q", bound)
	}
}

func TestBwrapBindsTheMachinesOwnResolverWhenItIsUsable(t *testing.T) {
	dir := t.TempDir()
	host := filepath.Join(dir, "host-resolv.conf")
	if err := os.WriteFile(host, []byte("nameserver 192.0.2.53\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	driver := NewBwrap(t.TempDir())
	driver.resolver = resolverFiles{Host: host, Upstream: filepath.Join(dir, "absent")}
	got, err := driver.resolverFile(filepath.Join(dir, "state", "acornfall-test"))
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	if got != "" {
		t.Fatalf("a usable resolver needs no copy, got %q", got)
	}
	// And the argv falls back to the machine's own file.
	if resolverOrHost(bwrapConfig{}) != "/etc/resolv.conf" {
		t.Fatal("the fallback is the machine's own resolver file")
	}
}

// A workspace that asked to be kept alive keeps the value that says so: the default is only for a
// policy that named nothing.
func TestBwrapKeepsTheIdleTimeoutAPolicyNames(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	workspace := t.TempDir()
	if _, err := driver.writeConfig(Policy{Workspace: workspace, IdleTimeout: KeptAliveIdleTimeout}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	config, err := driver.readConfig(Name(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if config.IdleTimeout != KeptAliveIdleTimeout {
		t.Fatalf("idle timeout: %q, want %q", config.IdleTimeout, KeptAliveIdleTimeout)
	}
}

// A workspace with no private-networks grant is given routes that refuse them, and pasta carries
// its DNS, so nothing about the machine's own networks is reachable from inside.
func TestBwrapRefusesPrivateNetworksWithoutAGrant(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	config := bwrapConfig{
		Workspace: "/work/ws", Rootfs: "/state/ws/rootfs", Home: "/state/ws/home",
		Bwrap: "/usr/bin/bwrap", Supervisor: "/install/acornfall",
		Run: "/state/ws/run", GuestRun: guestRunDir, GuestSocket: guestSocketPath,
		Network: bwrapNetwork{
			Mode: "pasta", Address: "10.0.2.15/24", Gateway: "10.0.2.2",
			Pasta: "/usr/bin/pasta",
		},
	}
	argv, err := driver.bootArgv(config)
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		// The flag asks for the routes; the list itself lives where the routes are installed, so
		// this asserts the request rather than repeating the list in a second place.
		"acornfall netns --address 10.0.2.15/24 --gateway 10.0.2.2 --refuse-private",
		"--dns-forward 10.0.2.3",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("a sandbox without a grant should carry %q:\n%s", want, joined)
		}
	}
}

// A workspace that asked for private networks gets the machine's networks and the machine's
// resolver: the grant is the difference, and it is visible in the argv.
func TestBwrapAGrantReachesPrivateNetworks(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	config := bwrapConfig{
		Workspace: "/work/ws", Rootfs: "/state/ws/rootfs", Home: "/state/ws/home",
		Bwrap: "/usr/bin/bwrap", Supervisor: "/install/acornfall",
		Run: "/state/ws/run", GuestRun: guestRunDir, GuestSocket: guestSocketPath,
		Network: bwrapNetwork{
			Mode: "pasta", Address: "10.0.2.15/24", Gateway: "10.0.2.2",
			Pasta: "/usr/bin/pasta", Private: true,
		},
	}
	argv, err := driver.bootArgv(config)
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "--refuse-private") {
		t.Errorf("a granted workspace should have no refused ranges:\n%s", joined)
	}
	if strings.Contains(joined, "--dns-forward") {
		t.Errorf("a granted workspace reads the machine's resolver, not pasta's:\n%s", joined)
	}
}

// The sandbox's resolver file is written by acornfall when the machine's is unreachable from
// inside: pasta's address, forwarded to the machine's own resolver.
func TestBwrapWritesTheResolverItsNetworkCanReach(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	workspace := t.TempDir()
	name := Name(workspace)
	path, err := driver.resolveResolv(name, bwrapNetwork{Mode: "pasta"})
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	if path == "" {
		t.Fatal("a sandbox without a grant should read a file of its own")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "nameserver "+sandboxDNS) {
		t.Fatalf("the resolver should name pasta's forwarder, got %q", content)
	}
}
