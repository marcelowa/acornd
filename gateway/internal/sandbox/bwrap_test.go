package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/netns"
	"github.com/marcelowa/acornd/gateway/internal/sandbox/sandboxtest"
	"github.com/marcelowa/acornd/gateway/internal/supervise"
)

// TestMain lets this binary be the sandbox's supervisor, which is what a started sandbox runs.
// The driver binds this executable into the sandbox and runs it with the supervisor's arguments;
// a test binary that ignored them would start a second test run inside the sandbox instead.
func TestMain(m *testing.M) {
	// The pasta probe asks this binary to run `help`: it must exit without starting the suite,
	// or the probe recurses into the tests it is meant to gate.
	if len(os.Args) > 1 && os.Args[1] == "help" {
		os.Exit(0)
	}
	// A live egress test runs this binary as the sandbox's netns helper, exactly as the product
	// runs acornd; the configuration is the same netns.Command.
	if len(os.Args) > 2 && os.Args[1] == "internal" && os.Args[2] == "netns" {
		command, err := netns.Command(os.Args[3:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "acornd test binary: netns:", err)
			os.Exit(2)
		}
		if err := syscall.Exec(command[0], command, os.Environ()); err != nil {
			fmt.Fprintln(os.Stderr, "acornd test binary: netns:", err)
			os.Exit(2)
		}
	}
	if len(os.Args) > 2 && os.Args[1] == "internal" && os.Args[2] == "sandbox-supervisor" {
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

func testBwrap(t *testing.T, dataDir string) (*Bwrap, *fakeBwrap) {
	t.Helper()
	runner := &fakeBwrap{version: "bubblewrap 0.11.0"}
	driver := NewBwrap(dataDir).
		WithRunner(runner).
		WithImageSource(sandboxtest.FakeImage).
		WithDevicePaths("", "").
		WithLookPath(func(string) (string, error) { return "/usr/bin/bwrap", nil })
	driver.pasta = func(string) error { return nil }
	return driver, runner
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
	dataDir := t.TempDir()
	driver, runner := testBwrap(t, dataDir)
	runner.probe = errors.New("exit status 1")
	err := driver.Usable()
	if err == nil {
		t.Fatal("Usable accepted a machine that refuses a user namespace")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error is not a refusal: %v", err)
	}
	// The sentence names the reason and the fix, because "sandbox Unavailable" is not one.
	for _, want := range []string{"user namespace", "AppArmor", "sysctl -w kernel.apparmor_restrict_unprivileged_userns=0"} {
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
	dataDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(filepath.Join(dataDir, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	driver, _ := testBwrap(t, dataDir)
	policy := Policy{
		Workspace: workspace,
		Image:     "debian:stable-slim",
		Egress:    false,
		Global:    []string{filepath.Join(dataDir, "skills"), filepath.Join(dataDir, "absent")},
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
	if len(config.ReadOnly) != 1 || config.ReadOnly[0] != filepath.Join(dataDir, "skills") {
		t.Fatalf("read-only mounts: %v", config.ReadOnly)
	}
	// The private home is inside the engine's folder, so removing the sandbox removes it.
	if !strings.HasPrefix(config.DataDir, driver.StateDir()) {
		t.Fatalf("home %q is not inside %q", config.DataDir, driver.StateDir())
	}
	// The workspace's own copy of its image, and the apt line that makes a container root
	// usable under a single-mapping user namespace.
	if _, err := os.Stat(filepath.Join(config.Rootfs, "etc", "os-release")); err != nil {
		t.Fatalf("the rootfs was not copied from the image: %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.Rootfs, "etc", "apt", "apt.conf.d", "99-acornd")); err != nil {
		t.Fatalf("the apt sandbox line was not written: %v", err)
	}
	info, err := os.Stat(config.DataDir)
	if err != nil || !info.IsDir() {
		t.Fatalf("home was not created: %v", err)
	}
}

// A pasta that leaves a process behind holding its output must be a refusal, not a hang. The
// probe runs before a sandbox starts, and a CombinedOutput that waits for every writer is a
// workspace that never opens — the two minutes a dev workspace spent on this were the same bug as
// a killed run that never ends, one layer down.
func TestAPastaThatLeavesAProcessBehindIsRefusedNotHung(t *testing.T) {
	script := filepath.Join(t.TempDir(), "pasta")
	// It holds stdout open in another session and ignores the kill, like a pasta waiting for a
	// device that is not there.
	body := "#!/bin/sh\nsetsid sh -c 'sleep 6' &\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write the fake pasta: %v", err)
	}
	start := time.Now()
	if err := probePastaWithin(script, 200*time.Millisecond); err == nil {
		t.Fatal("a pasta that never answers should be refused")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("the probe waited %s; it should bound the wait", elapsed)
	}
}

// Egress is carried by pasta: the sandbox gets an address inside a namespace of its own, and the
// host's own stack is not what a command is on.
func TestBwrapCreateCarriesEgressWithPasta(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	driver, _ := testBwrap(t, dataDir)
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
	if config.Network.Address != SandboxAddress || config.Network.Gateway != SandboxGateway {
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
			WithImageSource(sandboxtest.FakeImage).
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
	dataDir := t.TempDir()
	workspace := t.TempDir()
	driver, _ := testBwrap(t, dataDir)
	policy := Policy{Workspace: workspace}
	for i := range 2 {
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
	dataDir := t.TempDir()
	workspace := t.TempDir()
	// The socket's folder is outside the store, so the test picks where it goes rather than
	// writing into the machine's own runtime folder.
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "run"))
	driver, _ := testBwrap(t, dataDir)
	config, err := driver.writeConfig(Policy{Workspace: workspace}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(driver.StateDir(), Name(workspace)+".json")
	if err := driver.Remove(context.Background(), Name(workspace)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config survived remove: %v", err)
	}
	// The socket's folder is named only by the config, so it has to be read before the config
	// goes: this is the leftover that removal showed on a real install.
	if _, err := os.Stat(config.Run); !os.IsNotExist(err) {
		t.Fatalf("remove left the socket folder behind: %v", err)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("remove took the workspace with it: %v", err)
	}
	if _, err := os.Stat(filepath.Join(driver.StateDir(), Name(workspace))); !os.IsNotExist(err) {
		t.Fatalf("remove left the sandbox's own folder behind: %v", err)
	}
	// Removing what is not there is already the wanted state.
	if err := driver.Remove(context.Background(), "acornd-absent"); err != nil {
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
		DataDir:     "/state/ws/home",
		Hidden:      "/home/me/.acornd",
		ReadOnly:    []string{"/home/me/.agents"},
		Bwrap:       "/usr/bin/bwrap",
		Supervisor:  "/install/acornd",
		Run:         "/state/ws/run",
		GuestRun:    GuestRunDir,
		GuestSocket: GuestSocketPath,
		Network:     SandboxNetwork{Mode: "off"},
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
		"--bind /state/ws/rootfs /",
		"--uid 0 --gid 0",
		// A root still needs the machine's resolver, read-only.
		"--ro-bind /etc/resolv.conf /etc/resolv.conf",
		// The private home, the workspace at its own path, and the socket's folder on both sides.
		"--bind /state/ws/home /root",
		"--bind /work/ws /work/ws",
		"--bind /state/ws/run /run/acornd",
		"--ro-bind /home/me/.agents /home/me/.agents",
		// The supervisor is this binary, bound in, and it is what the sandbox runs.
		"--ro-bind /install/acornd /usr/local/bin/acornd",
		"-- /usr/local/bin/acornd internal sandbox-supervisor --socket /run/acornd/s.sock",
		// The same policy value the microVM engine is given: both engines answer "stop when idle"
		// the same way, and a kept-Alive workspace asks for a timeout that never fires.
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
		DataDir:     "/state/ws/home",
		Bwrap:       "/usr/bin/bwrap",
		Supervisor:  "/install/acornd",
		Run:         "/state/ws/run",
		GuestRun:    GuestRunDir,
		GuestSocket: GuestSocketPath,
		Network: SandboxNetwork{
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
		// would be talking to the acornd that runs it.
		"-t none -u none -T none -U none", "--no-map-gw",
		"-a 10.0.2.15 -n 24 -g 10.0.2.2",
		// pasta configures neither the address nor the route, so the sandbox does it from inside
		// the namespace pasta made: this binary over netlink, not a shell and not iproute2.
		"/install/acornd internal netns --address 10.0.2.15/24 --gateway 10.0.2.2",
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
	dataDir := filepath.Join(t.TempDir(), strings.Repeat("very-long-folder-name/", 4))
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	driver, _ := testBwrap(t, dataDir)
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
	// The bwrap tier runs this: it boots a real sandbox, and the mutation sweep runs the
	// package's tests once per mutant. A boot there is a job measured in minutes.
	if os.Getenv("ACORND_BWRAP_ONLY") == "" {
		t.Skip("the bwrap tier runs this: it boots a real sandbox")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("no bubblewrap on this machine")
	}
	dataDir := t.TempDir()
	workspace := t.TempDir()
	driver := NewBwrap(dataDir).
		WithImageSource(sandboxtest.FakeImage).
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
	pid := driver.pid(Name(workspace))
	if err := driver.Remove(context.Background(), Name(workspace)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if instances, _ := driver.List(context.Background()); len(instances) != 0 {
		t.Fatalf("a removed sandbox is gone: %+v", instances)
	}
	if pid > 0 && Alive(pid) {
		t.Fatalf("remove left the sandbox process %d running", pid)
	}
}

func TestBwrapSubstitutesSecretsThroughItsProxy(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	if driver.SecretDelivery() != DeliverySubstitute {
		t.Fatalf("delivery: %v, want substitute", driver.SecretDelivery())
	}
	// The values stay in the proxy control runs for this workspace. A worker that held them would
	// hand them to every command it spawned, a hook, and a crash report.
	if driver.SecretEnvInWorker() {
		t.Fatal("the worker must not carry a secret's value")
	}
	change, err := driver.SecretsChanged(context.Background(), "x", []Secret{{Name: "K", Value: "v", Hosts: []string{"api.example.com"}}})
	if err != nil {
		t.Fatalf("change: %v", err)
	}
	if change.RestartRequired {
		t.Fatal("a grant swap under a running proxy should not need a restart")
	}
}

// A workspace with a proxy gets one port into its own loopback and a bundle it trusts. The two
// cases are asserted as arguments, because pasta and bubblewrap read the arguments and nothing
// else about this decision reaches them.
func TestABwrapWorkspaceWithASecretsProxyCarriesOnePortAndItsCA(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	base := bwrapConfig{
		Workspace:   "/work/ws",
		Rootfs:      "/state/ws/rootfs",
		DataDir:     "/state/ws/home",
		Bwrap:       "/usr/bin/bwrap",
		Supervisor:  "/install/acornd",
		Run:         "/state/ws/run",
		GuestRun:    GuestRunDir,
		GuestSocket: GuestSocketPath,
		Network: SandboxNetwork{
			Mode: "pasta", Address: "10.0.2.15/24", Gateway: "10.0.2.2",
			Pasta: "/usr/bin/pasta",
		},
	}

	withProxy := base
	withProxy.SecretProxy = &SandboxSecretProxy{
		URL: "http://acornd:a-token@127.0.0.1:41234", BundleFile: "/state/ws/ca-bundle.pem",
		Bundle: GuestCABundle, Port: 41234,
	}
	argv, err := driver.bootArgv(withProxy)
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "-t none -u none -T 41234 -U none") {
		t.Errorf("pasta should carry exactly the proxy's port:\n%s", joined)
	}
	if !strings.Contains(joined, "--ro-bind /state/ws/ca-bundle.pem "+GuestCABundle) {
		t.Errorf("the bundle should be bound read-only over the image's roots:\n%s", joined)
	}

	without, err := driver.bootArgv(base)
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	plain := strings.Join(without, " ")
	if !strings.Contains(plain, "-t none -u none -T none -U none") {
		t.Errorf("a workspace with no proxy should forward nothing:\n%s", plain)
	}
	if strings.Contains(plain, "ca-bundle.pem") {
		t.Errorf("a workspace with no proxy has no bundle to bind:\n%s", plain)
	}
}

// The trust bundle is the image's own roots and this install's CA, in that order: a client that
// trusted only the proxy would lose every other TLS connection a command makes.
func TestTheTrustBundleIsTheImagesRootsAndThisInstallsCA(t *testing.T) {
	rootfs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootfs, "etc/ssl/certs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootfs, GuestCABundle), []byte("-- an image root --"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	path := filepath.Join(t.TempDir(), "bundle.pem")
	if err := writeTrustBundle(path, imageRoots(rootfs), []byte("-- this install's CA --")); err != nil {
		t.Fatalf("bundle: %v", err)
	}
	bundle, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := "-- an image root --\n-- this install's CA --\n"
	if string(bundle) != want {
		t.Errorf("bundle is %q, want %q", bundle, want)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("the bundle is %v, and a client inside has to read it: %v", info, err)
	}
}

// The nested microVM tier: the host's engine, the host's store, and a real microVM inside a
// bwrap sandbox with the devices and caps a workspace gets by default. It is in the sandbox
// tier because it boots a real microVM; the fast suite skips it with -short.
//
// The switch is the one the rest of the live suite already asks for, and this test has a reason
// they do not: the mutation sweep runs this package once per mutant, so a boot here is a boot in
// every one of several hundred passes. It used to ask the machine instead of the caller — "is
// bubblewrap installed" — which was right for as long as the runner image shipped none. The
// 26.04 image ships one, the sweep started booting microVMs, and the boot aborted on a /dev/kvm
// the job had no reason to make readable; the abort reached the report as a mutation survivor.
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
		// account, the host's / belongs to nobody this process can name, and 0.7.13 refuses an
		// engine store when an ancestor could be substituted by another account — the refusal
		// reads "catalog ancestor / permits path substitution". A root bubblewrap makes is the
		// fix, and it is also the shape the product's own bwrap engine has: a rootfs, with what
		// the engine needs from the host bound into it read-only.
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

func TestBwrapDoesNotClaimNestingWithoutKVM(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	support := driver.Nesting(context.Background())
	if support.Available {
		t.Fatal("bwrap claims a hypervisor can start without a /dev/kvm to bind")
	}
	if !strings.Contains(support.Reason, "/dev/kvm") {
		t.Fatalf("reason %q does not say what is missing", support.Reason)
	}
}

func TestBwrapConfigRecordsNestedFromTheParent(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	t.Setenv(nestedEnv, "1")
	config, err := driver.writeConfig(Policy{Workspace: t.TempDir()}, t.TempDir())
	if err != nil {
		t.Fatalf("write config: %v", err)
	}
	if !config.Nested {
		t.Fatal("a bwrap engine inside a parent sandbox must record that it is nested")
	}
}

func TestBwrapConfigRecordsAWorkspaceThatMustNotNest(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	config, err := driver.writeConfig(Policy{Workspace: t.TempDir(), DisableVMNesting: true}, t.TempDir())
	if err != nil {
		t.Fatalf("write config: %v", err)
	}
	if !config.DisableNesting {
		t.Fatal("a workspace that disables nesting wrote a config that allows it")
	}
}

func TestBwrapClaimsNestingWhenKVMIsBound(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	kvm := filepath.Join(t.TempDir(), "kvm")
	if err := os.WriteFile(kvm, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	driver.WithDevicePaths("", kvm)
	if support := driver.Nesting(context.Background()); !support.Available {
		t.Fatalf("a machine with /dev/kvm should be able to nest: %+v", support)
	}
}

func TestBwrapPrepareWritesTheCurrentConfig(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	policy := Policy{Workspace: t.TempDir(), Devices: true}
	if err := driver.Prepare(context.Background(), policy, t.TempDir()); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	config, err := driver.readConfig(Name(policy.Workspace))
	if err != nil {
		t.Fatalf("read the config Prepare wrote: %v", err)
	}
	if !config.Devices {
		t.Fatal("Prepare wrote a config without the device grant")
	}
}

// pasta's forward and the trust bundle are argv at boot. A sandbox that started before its proxy
// existed cannot be repaired by rewriting the file; adding the first proxy, removing the last, or
// moving its port has to restart the sandbox. A rotation keeps the same listener and port.
func TestBwrapRestartsOnlyWhenTheProxyForwardChanges(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	workspace := t.TempDir()
	name := Name(workspace)
	write := func(proxy *SandboxSecretProxy) {
		t.Helper()
		raw, err := json.Marshal(bwrapConfig{Workspace: workspace, SecretProxy: proxy})
		if err != nil {
			t.Fatal(err)
		}
		path := driver.configPath(name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	policy := func(port int) Policy {
		return Policy{Workspace: workspace, SecretProxy: &SecretProxy{
			URL: fmt.Sprintf("http://acornd:token@127.0.0.1:%d", port),
		}}
	}

	write(nil)
	if restart, err := driver.RestartRequired(context.Background(), Policy{Workspace: workspace}); err != nil || restart {
		t.Fatalf("no proxy on either side is not a restart: %v, %v", restart, err)
	}
	if restart, err := driver.RestartRequired(context.Background(), policy(21606)); err != nil || !restart {
		t.Fatalf("the first proxy needs the sandbox started again: %v, %v", restart, err)
	}

	write(&SandboxSecretProxy{Port: 21606})
	if restart, err := driver.RestartRequired(context.Background(), policy(21606)); err != nil || restart {
		t.Fatalf("the same forward is a live rotation: %v, %v", restart, err)
	}
	if restart, err := driver.RestartRequired(context.Background(), policy(21607)); err != nil || !restart {
		t.Fatalf("a moved forward needs a new sandbox: %v, %v", restart, err)
	}
	if restart, err := driver.RestartRequired(context.Background(), Policy{Workspace: workspace}); err != nil || !restart {
		t.Fatalf("removing the last proxy needs the forward taken away: %v, %v", restart, err)
	}
}

func TestBwrapBindsDevicesAndGrantsNestedEnginesTheCaps(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	tun := filepath.Join(t.TempDir(), "tun")
	kvm := filepath.Join(t.TempDir(), "kvm")
	for _, path := range []string{tun, kvm} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	driver.WithDevicePaths(tun, kvm)
	policy := Policy{Workspace: t.TempDir(), Devices: true}
	config, err := driver.writeConfig(policy, t.TempDir())
	if err != nil {
		t.Fatalf("write config: %v", err)
	}
	if !config.Devices {
		t.Fatal("the config forgot the device grant")
	}
	argv, err := driver.sandboxArgv(config)
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"--dev-bind " + tun + " " + tun,
		"--dev-bind " + kvm + " " + kvm,
		"--cap-add ALL",
		"--setenv " + nestedEnv + " 1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv does not contain %q: %v", want, argv)
		}
	}
}

func TestBwrapNestedEgressReusesPastaUserNamespace(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	config := bwrapConfig{
		Bwrap:       "/usr/bin/bwrap",
		Rootfs:      "/rootfs",
		Network:     SandboxNetwork{Mode: "pasta", Address: SandboxAddress, Gateway: SandboxGateway, Pasta: "/usr/bin/pasta"},
		Nested:      true,
		DataDir:     "/home",
		Workspace:   "/work",
		Run:         "/run",
		GuestRun:    GuestRunDir,
		Supervisor:  "/usr/local/bin/acornd",
		GuestSocket: GuestSocketPath,
	}
	argv, err := driver.sandboxArgv(config)
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "--unshare-all") {
		t.Error("a nested sandbox with egress must not unshare a user namespace of its own")
	}
	if strings.Contains(joined, "--uid 0") || strings.Contains(joined, "--gid 0") {
		t.Error("a nested sandbox with egress runs in pasta's root, so it must not remap the uid")
	}
	for _, want := range []string{"--unshare-pid", "--unshare-uts", "--unshare-ipc", "--unshare-cgroup", "--share-net"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv does not contain %q: %v", want, argv)
		}
	}
}

// The same question as above, with the network off: a nested sandbox still runs in the user
// namespace its parent made, because asking for one of its own is refused at the uid map when the
// parent holds CAP_SYS_ADMIN and no CAP_SETUID. "Off" is an empty network namespace, not the
// parent's, or the policy would say off and the sandbox would still have the workspace's egress.
func TestBwrapNestedWithoutEgressReusesTheUserNamespace(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	config := bwrapConfig{
		Bwrap:       "/usr/bin/bwrap",
		Rootfs:      "/rootfs",
		Network:     SandboxNetwork{Mode: NetworkOffName},
		Nested:      true,
		DataDir:     "/home",
		Workspace:   "/work",
		Run:         "/run",
		GuestRun:    GuestRunDir,
		Supervisor:  "/usr/local/bin/acornd",
		GuestSocket: GuestSocketPath,
	}
	argv, err := driver.sandboxArgv(config)
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "--unshare-all") {
		t.Error("a nested sandbox must not unshare a user namespace of its own")
	}
	if strings.Contains(joined, "--uid 0") || strings.Contains(joined, "--gid 0") {
		t.Error("a nested sandbox runs in its parent's root, so it must not remap the uid")
	}
	for _, want := range []string{"--unshare-pid", "--unshare-uts", "--unshare-ipc", "--unshare-cgroup", "--unshare-net"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv does not contain %q: %v", want, argv)
		}
	}
	if strings.Contains(joined, "--share-net") {
		t.Error("a sandbox with no egress must not share its parent's network")
	}
}

// The unit test above pins the argv; this runs it against the parent a nested acornd actually has.
// Measured inside pasta on Debian 13, bubblewrap 0.12.0: the process is uid 0 with CAP_SYS_ADMIN,
// CAP_NET_ADMIN and CAP_NET_BIND_SERVICE and no CAP_SETUID. `--unshare-all --uid 0` therefore dies
// with "setting up uid map: Operation not permitted", while the reuse path starts and leaves the
// sandbox a network namespace with nothing in it but loopback.
func TestANestedSandboxWithNoEgressStartsInsidePasta(t *testing.T) {
	if os.Getenv("ACORND_BWRAP_ONLY") == "" {
		// Not a capability of this package's suite: pasta nests one user namespace inside another,
		// which a machine that restricts unprivileged user namespaces refuses, so this runs where
		// the bwrap tier has set the host up for it (make test-bwrap).
		t.Skip("the bwrap tier runs this: it needs the machine's user-namespace switch")
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("no bubblewrap on this machine")
	}
	pasta, err := exec.LookPath("pasta")
	if err != nil {
		t.Skip("no pasta on this machine")
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Skip("no /dev/net/tun to hand pasta")
	}
	if err := NewBwrap(t.TempDir()).Usable(); err != nil {
		t.Skipf("this machine will not make a user namespace: %v", err)
	}

	config := bwrapConfig{Nested: true, Network: SandboxNetwork{Mode: NetworkOffName}}
	inner := append([]string{bwrap, "--ro-bind", "/", "/", "--proc", "/proc"}, isolationArgs(config)...)
	inner = append(inner, "--", "/bin/sh", "-c",
		`test "$(tail -n +3 /proc/net/dev | cut -d: -f1 | tr -d ' ')" = lo`)

	// pasta is the parent the fix is about: it makes the user namespace a nested acornd lives in,
	// and its sandbox holds the capabilities named above. Running it directly is also what a
	// gateway does, so this needs no outer bubblewrap of its own.
	argv := append([]string{
		"-q", "-t", "none", "-u", "none", "-T", "none", "-U", "none", "--no-map-gw", "--",
	}, inner...)
	if output, err := exec.Command(pasta, argv...).CombinedOutput(); err != nil {
		t.Fatalf("a nested sandbox with no egress did not start: %v\n%s\nargv: %q", err, output, argv)
	}
}

// A workspace that disables nesting is a leaf: its sandbox has a user namespace of its own, and the
// kernel is told to refuse another one inside it. --disable-userns is refused without an explicit
// --unshare-user — it checks the flag, not the --unshare-all bundle — and
// --assert-userns-disabled makes a kernel that cannot install the filter fail the boot.
func TestBwrapLeafSandboxRefusesAnotherUserNamespace(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	config := bwrapConfig{
		Bwrap:          "/usr/bin/bwrap",
		Rootfs:         "/rootfs",
		Network:        SandboxNetwork{Mode: NetworkOffName},
		DisableNesting: true,
		DataDir:        "/home",
		Workspace:      "/work",
		Run:            "/run",
		GuestRun:       GuestRunDir,
		Supervisor:     "/usr/local/bin/acornd",
		GuestSocket:    GuestSocketPath,
	}
	argv, err := driver.sandboxArgv(config)
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"--unshare-all",
		"--unshare-user", "--disable-userns", "--assert-userns-disabled",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv does not contain %q: %v", want, argv)
		}
	}
}

// A nested sandbox cannot carry the flag: bubblewrap refuses --disable-userns without
// --unshare-user, and a nested sandbox must not unshare one (ADR 0030). Its parent's policy
// decided, and a parent that is a leaf never starts a nested sandbox at all.
func TestBwrapNestedSandboxIsNotToldToDisableUserns(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	config := bwrapConfig{
		Bwrap:          "/usr/bin/bwrap",
		Rootfs:         "/rootfs",
		Network:        SandboxNetwork{Mode: "pasta"},
		Nested:         true,
		DisableNesting: true,
		DataDir:        "/home",
		Workspace:      "/work",
		Run:            "/run",
		GuestRun:       GuestRunDir,
		Supervisor:     "/usr/local/bin/acornd",
		GuestSocket:    GuestSocketPath,
	}
	argv, err := driver.sandboxArgv(config)
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	joined := strings.Join(argv, " ")
	for _, unwanted := range []string{"--unshare-user", "--disable-userns", "--assert-userns-disabled"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("a nested sandbox was given %q: %v", unwanted, argv)
		}
	}
}

// The unit tests above pin the argv; this checks the kernel agrees. The control is the same
// sandbox without the flag: where that one already refuses a nested namespace, the machine has
// nothing to prove and the test skips.
func TestALeafSandboxRefusesAUserNamespaceInsideIt(t *testing.T) {
	if os.Getenv("ACORND_BWRAP_ONLY") == "" {
		t.Skip("the bwrap tier runs this: it needs the machine's user-namespace switch")
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("no bubblewrap on this machine")
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("no unshare on this machine")
	}
	if err := NewBwrap(t.TempDir()).Usable(); err != nil {
		t.Skipf("this machine will not make a user namespace: %v", err)
	}
	run := func(disable bool) ([]byte, error) {
		config := bwrapConfig{DisableNesting: disable, Network: SandboxNetwork{Mode: NetworkOffName}}
		argv := append([]string{"--ro-bind", "/", "/", "--proc", "/proc"}, isolationArgs(config)...)
		argv = append(argv, "--", "/bin/sh", "-c", "unshare -Ur true")
		return exec.Command(bwrap, argv...).CombinedOutput()
	}
	if output, err := run(false); err != nil {
		t.Skipf("this machine will not make a nested user namespace anyway: %v\n%s", err, output)
	}
	if output, err := run(true); err == nil {
		t.Fatalf("a leaf sandbox let a user namespace in: %s", output)
	}
}

func TestAPastaThatCannotMapItsUserNamespaceIsRefused(t *testing.T) {
	script := filepath.Join(t.TempDir(), "pasta")
	body := "#!/bin/sh\necho \"Couldn't configure user mappings\" >&2\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := probePastaWithin(script, time.Second); err == nil {
		t.Fatal("pasta exited 0 but never made its user namespace; the probe must refuse it")
	}
}

// The program the probe hands pasta is the point of the probe: `/bin/true` is exactly what an
// AppArmor profile for pasta allows, so a probe that ran it said the network worked while every
// sandbox with one refused to start the supervisor. This asks for the same binary a sandbox does.
func TestThePastaProbeHandsPastaThisBinaryAndNotBinTrue(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "argv")
	script := filepath.Join(dir, "pasta")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + record + "\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := probePastaWithin(script, time.Second); err != nil {
		t.Fatalf("the probe: %v", err)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The separator, then the program and its one argument.
	if len(args) < 3 {
		t.Fatalf("pasta was given %v", args)
	}
	if args[len(args)-2] != executable || args[len(args)-1] != "help" {
		t.Errorf("the probe should run %s help, got %v", executable, args[len(args)-2:])
	}
	for _, argument := range args {
		if argument == "/bin/true" {
			t.Fatalf("the probe still runs /bin/true, which an AppArmor profile for pasta allows: %v", args)
		}
	}
}

// pasta refusing to start the program it was given is its own sentence, and exit 0 does not make
// it a working network: the narrowed AppArmor profile is the cause a person needs named.
func TestAPastaThatCannotStartTheProgramItIsGivenIsRefused(t *testing.T) {
	script := filepath.Join(t.TempDir(), "pasta")
	body := "#!/bin/sh\necho \"Failed to start command or shell: Permission denied\" >&2\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	err := probePastaWithin(script, time.Second)
	if err == nil {
		t.Fatal("pasta could not start the supervisor; the probe must refuse it")
	}
	if !strings.Contains(err.Error(), "/usr/bin") || !strings.Contains(err.Error(), "acornd setup") {
		t.Errorf("the refusal should name the profile and the fix: %v", err)
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
	if !strings.Contains(strings.Join(driver.Env(), " "), "ACORND_BWRAP_STATE="+driver.StateDir()) {
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
	driver.resolver = ResolverFiles{Host: host, Upstream: upstream}
	workspace := filepath.Join(dir, "state", "acornd-test")
	got, err := resolverFileFor(driver.resolver, workspace)
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
	driver.resolver = ResolverFiles{Host: host, Upstream: filepath.Join(dir, "absent")}
	got, err := resolverFileFor(driver.resolver, filepath.Join(dir, "state", "acornd-test"))
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

// A workspace that asked to be kept Alive keeps the value that says so: the default is only for a
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
		Workspace: "/work/ws", Rootfs: "/state/ws/rootfs", DataDir: "/state/ws/home",
		Bwrap: "/usr/bin/bwrap", Supervisor: "/install/acornd",
		Run: "/state/ws/run", GuestRun: GuestRunDir, GuestSocket: GuestSocketPath,
		Network: SandboxNetwork{
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
		"acornd internal netns --address 10.0.2.15/24 --gateway 10.0.2.2 --refuse-private",
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
		Workspace: "/work/ws", Rootfs: "/state/ws/rootfs", DataDir: "/state/ws/home",
		Bwrap: "/usr/bin/bwrap", Supervisor: "/install/acornd",
		Run: "/state/ws/run", GuestRun: GuestRunDir, GuestSocket: GuestSocketPath,
		Network: SandboxNetwork{
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

// The sandbox's resolver file is written by acornd when the machine's is unreachable from
// inside: pasta's address, forwarded to the machine's own resolver.
func TestBwrapWritesTheResolverItsNetworkCanReach(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	workspace := t.TempDir()
	name := Name(workspace)
	path, err := ResolveResolvFor(driver.workspaceDir(name), driver.resolver, "pasta", false)
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
	if !strings.Contains(string(content), "nameserver "+SandboxDNS) {
		t.Fatalf("the resolver should name pasta's forwarder, got %q", content)
	}
}

// The egress probe answers the question a start will ask: can pasta make a namespace here. It is
// not a version check, and its refusal names the fix the way every other refusal does.
func TestBwrapEgressProbeSaysWhatToDo(t *testing.T) {
	// No pasta at all: the package is the fix.
	driver := NewBwrap(t.TempDir()).
		WithLookPath(func(string) (string, error) { return "", errors.New("not found") })
	if err := driver.Egress(); err == nil || !strings.Contains(err.Error(), "apt install passt") {
		t.Fatalf("no pasta: %v, want the package that provides it", err)
	}
	// pasta present but unable to make a namespace: the kernel policy is the fix, so the probe
	// has to fail rather than ask for a version.
	driver = NewBwrap(t.TempDir()).
		WithLookPath(func(string) (string, error) { return "/usr/bin/pasta", nil })
	driver.pasta = func(string) error { return errors.New("setting up user namespace: Operation not permitted") }
	if err := driver.Egress(); err == nil || !strings.Contains(err.Error(), "Operation not permitted") {
		t.Fatalf("a refused namespace should be passed through with its reason: %v", err)
	}
}

// The sandbox's own processes are started with three variables, not with the gateway's environment.
// What they inherited instead was everything acornd was started with, and a command inside could
// read it at /proc/<pid>/environ: a workspace is not where the machine's credentials live.
func TestTheSandboxProcessGetsItsOwnEnvironment(t *testing.T) {
	t.Parallel()
	env := sandboxProcessEnv(bwrapConfig{DataDir: "/state/ws/home"})
	want := []string{"PATH=" + ProcessPath, "HOME=/state/ws/home", "LANG=C.UTF-8"}
	if !slices.Equal(env, want) {
		t.Errorf("the sandbox's environment is %q, want %q", env, want)
	}
	if strings.Contains(strings.Join(env, " "), "ACORND") {
		t.Errorf("nothing of this install should reach the sandbox's own environment: %q", env)
	}
	if empty := sandboxProcessEnv(bwrapConfig{}); empty[1] != "HOME=/" {
		t.Errorf("a config with no home still needs one: %q", empty)
	}
}

// --clearenv comes first. bubblewrap applies its flags in order, and the devices a sandbox was
// granted add --setenv flags of their own — a clear after them would clear what they set.
func TestTheSandboxArgvClearsTheEnvironmentFirst(t *testing.T) {
	t.Parallel()
	driver, _ := testBwrap(t, t.TempDir())
	argv, err := driver.sandboxArgv(bwrapConfig{
		Bwrap: "/usr/bin/bwrap", Workspace: "/work/ws", DataDir: "/state/ws/home",
		Run: "/state/ws/run", GuestRun: GuestRunDir, GuestSocket: GuestSocketPath,
		Supervisor: "/install/acornd",
	})
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	if len(argv) < 2 || argv[1] != "--clearenv" {
		t.Fatalf("the sandbox's argv should clear the environment before anything sets one: %q", argv)
	}
}

// A youki container is nested but cannot make namespaces: it holds CAP_SETFCAP for the uid map and
// no CAP_SYS_ADMIN for anything else. The sandbox still records itself as nested (the worker reads
// that), but bubblewrap asks for a user namespace of its own, which is the one thing that
// capability makes possible. Without this a nested acornd in a youki workspace dies at
// "Creating new namespace failed: Operation not permitted".
func TestANestedSandboxWithoutSysAdminKeepsAUserNamespaceOfItsOwn(t *testing.T) {
	driver, _ := testBwrap(t, t.TempDir())
	for _, network := range []SandboxNetwork{
		{Mode: NetworkOffName},
		{Mode: "pasta", Address: SandboxAddress, Gateway: SandboxGateway, Pasta: "/usr/bin/pasta"},
	} {
		config := bwrapConfig{
			Bwrap:            "/usr/bin/bwrap",
			Rootfs:           "/rootfs",
			Network:          network,
			Nested:           true,
			OwnUserNamespace: true,
			DataDir:          "/home",
			Workspace:        "/work",
			Run:              "/run",
			GuestRun:         GuestRunDir,
			Supervisor:       "/usr/local/bin/acornd",
			GuestSocket:      GuestSocketPath,
		}
		argv, err := driver.sandboxArgv(config)
		if err != nil {
			t.Fatalf("argv: %v", err)
		}
		joined := strings.Join(argv, " ")
		for _, want := range []string{"--unshare-all", "--uid 0", "--gid 0"} {
			if !strings.Contains(joined, want) {
				t.Errorf("a nested sandbox without CAP_SYS_ADMIN needs %q: %v", want, argv)
			}
		}
		if network.Mode == NetworkOffName && strings.Contains(joined, "--share-net") {
			t.Errorf("a sandbox with no egress must not share its parent's network: %v", argv)
		}
		if network.Mode != NetworkOffName && !strings.Contains(joined, "--share-net") {
			t.Errorf("a sandbox with egress must share pasta's network: %v", argv)
		}
	}
}
