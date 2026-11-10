package bwrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/marcelowa/acornd/gateway/internal/sandbox"
	"net"
	"os"
	"os/exec"
	"path/filepath"

	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/supervise"
)

// Bwrap is the bubblewrap engine: user, mount, pid, network, ipc and uts namespaces built
// from an argv, with no runtime and no sandbox object. Every command is a sandbox; what it
// writes to the workspace is the only thing that survives it. See
// docs/adr/0023-bwrap-engine.md for the measurements and what it costs.
//
// Beta: the host root is visible read-only, the install folder is hidden by a tmpfs with the
// global resources bound back, and there is no sandbox lifecycle to stop. The config a
// command runs under is written to the engine's state folder, so a person can read what a
// workspace is allowed to be, and the worker reads the same file to build the argv.
type Bwrap struct {
	dataDir string
	// state is where one JSON file per workspace's config lives.
	state string
	// resolver paths are fields on Bwrap; this is the type they use.
	// runner runs bubblewrap. It is an interface so the argv can be asserted in tests, and
	// the probe can be answered without a user namespace.
	runner sandbox.Runner
	look   func(string) (string, error)
	// pull fetches an image into a directory. It is a field so a test can answer without a
	// registry, the same way the runner answers without a binary.
	pull sandbox.ImageSource
	// resolver is the machine's resolver file and the file its upstream servers are in. Paths are
	// fields so a test can answer for a machine that is not this one.
	resolver sandbox.ResolverFiles
	// pasta is the egress probe, a field so a test can answer without running one.
	pasta func(string) error
	// devices are the host paths a sandbox may be given. They are fields so a test can
	// answer for a machine that has neither without making the suite depend on this one.
	devices sandbox.HostDevices
	// overlay answers whether this machine can mount an overlay root inside a user namespace.
	// It is a field so a test can answer without one, and it is asked once per driver.
	overlay     func(string) bool
	overlayOnce sync.Once
	overlayOK   bool

	once    sync.Once
	version string
}

// NewBwrap builds the driver. Unlike the microVM engine it installs nothing: bubblewrap is a
// distribution package, and installing one is the operator's business, not a gateway's.
func NewBwrap(dataDir string) *Bwrap {
	driver := &Bwrap{
		dataDir:  dataDir,
		state:    filepath.Join(dataDir, "sandboxes", "bwrap"),
		look:     exec.LookPath,
		pull:     sandbox.PullImage,
		resolver: sandbox.ResolverFiles{Host: "/etc/resolv.conf", Upstream: "/run/systemd/resolve/resolv.conf"},
		devices:  sandbox.HostDevices{Tun: "/dev/net/tun", KVM: "/dev/kvm"},
	}
	driver.overlay = driver.overlaySupported
	return driver
}

// overlayEnabled is the probe's answer, taken once: the kernel, the filesystem, and the installed
// bubblewrap either can mount an overlay inside a user namespace or they cannot, and the answer
// does not change under a running gateway.
func (b *Bwrap) overlayEnabled(image string) bool {
	if b.overlay == nil {
		return false
	}
	b.overlayOnce.Do(func() { b.overlayOK = b.overlay(image) })
	return b.overlayOK
}

// overlaySupported mounts a throwaway overlay with the installed bubblewrap, exactly the shape a
// workspace's root would have. Reading /proc/filesystems would say overlayfs exists; only a mount
// inside the user namespace says this machine can use it. A failure is the copy fallback, not a
// broken sandbox.
func (b *Bwrap) overlaySupported(image string) bool {
	if err := os.MkdirAll(b.StateDir(), 0o700); err != nil {
		return false
	}
	base, err := sandbox.ImageBase(b.StateDir(), image, b.pull)
	if err != nil {
		return false
	}
	dir, err := os.MkdirTemp(b.StateDir(), "overlay-probe-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	upper := filepath.Join(dir, "upper")
	work := filepath.Join(dir, "work")
	for _, path := range []string{upper, work} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return false
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = b.run(ctx, "--unshare-all", "--overlay-src", base, "--overlay", upper, work, "/", "--", "/bin/true")
	return err == nil
}

// WithDevicePaths answers "does this machine have them" for a test, which must not depend on
// the device nodes of the machine running it.
func (b *Bwrap) WithDevicePaths(tun, kvm string) *Bwrap {
	b.devices = sandbox.HostDevices{Tun: tun, KVM: kvm}
	return b
}

// WithImageSource replaces the registry with a directory of its own, which is how the pull and
// the unpack are tested without a network.
func (b *Bwrap) WithImageSource(pull sandbox.ImageSource) *Bwrap {
	b.pull = pull
	return b
}

// WithRunner replaces the bubblewrap binary, which is how the driver is tested without one.
func (b *Bwrap) WithRunner(runner sandbox.Runner) *Bwrap {
	b.runner = runner
	return b
}

// WithLookPath answers "is it installed" for a test, which must not depend on this machine.
func (b *Bwrap) WithLookPath(look func(string) (string, error)) *Bwrap {
	b.look = look
	return b
}

// run drives bubblewrap. Nothing is inherited from the gateway's environment: the probe and
// the version read only what bubblewrap prints.
func (b *Bwrap) run(ctx context.Context, args ...string) (string, error) {
	if b.runner != nil {
		return b.runner.Run(ctx, args...)
	}
	command := exec.CommandContext(ctx, "bwrap", args...)
	output, err := command.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("bubblewrap: %s", sandbox.FirstLine(text))
	}
	return text, nil
}

// WithStateDir moves the engine's folder, as the microVM engine does.
func (b *Bwrap) WithStateDir(state string) *Bwrap {
	b.state = filepath.Join(state, "bwrap")
	return b
}

func (b *Bwrap) Name() string { return "bwrap" }

// Version is bubblewrap's own, read once. A machine without it reports no version rather
// than failing here: Installed and Usable are what say whether it can run.
func (b *Bwrap) Version() string {
	b.once.Do(func() {
		out, err := b.run(context.Background(), "--version")
		if err != nil {
			return
		}
		b.version = lastWord(out)
	})
	return b.version
}

// Isolated is true: bubblewrap separates what runs from the host with namespaces. It is a
// weaker boundary than a microVM — the kernel is shared — which is why the interface says
// which engine a workspace runs on rather than only that it is isolated.
func (b *Bwrap) Isolated() bool { return true }

// InstallDir is the binary's path, which is the honest answer to "where is it" for an engine
// that ships with the distribution.
func (b *Bwrap) InstallDir() string {
	path, err := b.look("bwrap")
	if err != nil {
		return ""
	}
	return path
}

func (b *Bwrap) StateDir() string { return b.state }

// Env is where the worker finds the configs this engine writes. The worker's client reads
// `<state>/<name>.json`, the same file a person can read.
func (b *Bwrap) Env() []string { return []string{"ACORND_BWRAP_STATE=" + b.state} }

func (b *Bwrap) Installed() bool {
	_, err := b.look("bwrap")
	return err == nil
}

// Install cannot install: bubblewrap is a package, and apt needs root. The refusal names the
// command, because "sandbox sandbox.Unavailable" with no next step is not an answer.
func (b *Bwrap) Install(context.Context, func(string, ...any)) error {
	if b.Installed() {
		return nil
	}
	return fmt.Errorf("%w: bubblewrap is not installed (install it: apt install bubblewrap)", sandbox.ErrUnavailable)
}

// Usable is a real probe: a user namespace is the one thing a bwrap sandbox cannot do without,
// and Ubuntu 24.04+ refuses unprivileged ones through AppArmor. The probe is one millisecond
// process, and it answers the question the machine will answer later anyway.
func (b *Bwrap) Usable() error {
	if !b.Installed() {
		return fmt.Errorf("%w: bubblewrap is not installed (install it: apt install bubblewrap)", sandbox.ErrUnavailable)
	}
	if out, err := b.run(context.Background(), probeArgs()...); err != nil {
		detail := strings.TrimSpace(out)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("%w: bubblewrap cannot create a user namespace here: %s (Ubuntu 23.10 and later restrict them with AppArmor; fix: sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0)", sandbox.ErrUnavailable, sandbox.LastLine(detail))
	}
	return nil
}

// Egress reports whether a workspace with its network on can run here: pasta carries it, and
// that means pasta can make a namespace on this machine. A machine without pasta can still run a
// workspace with its network off, which is why this is not Usable.
func (b *Bwrap) Egress() error { return sandbox.ProbeEgress(b.look, b.pasta) }

// Nesting reports whether a hypervisor can run in a bwrap sandbox. It can when the machine
// has /dev/kvm and the workspace grants devices: the device is bound into the sandbox and
// the capabilities nested pasta needs come with it.
func (b *Bwrap) Nesting(context.Context) sandbox.NestingSupport {
	if _, err := os.Stat(b.devices.KVM); err != nil {
		return sandbox.Unavailable("no /dev/kvm on this machine, so a hypervisor inside a bwrap sandbox cannot start")
	}
	return sandbox.NestingSupport{Available: true}
}

// config is what one workspace's commands run under, written for a person to read and for the
// worker to build an argv from.
type bwrapConfig struct {
	Engine    string `json:"engine"`
	Workspace string `json:"workspace"`
	Image     string `json:"image"`
	// Network is how a command reaches the network: a namespace with no interfaces, or one whose
	// traffic pasta forwards through the host. See sandbox.SandboxNetwork.
	Network sandbox.SandboxNetwork `json:"network"`
	// Rootfs is the workspace's own unpacked copy of its image, bound as `/`, when RootMode is
	// "copy". In overlay mode Rootfs is empty and Base, Upper and Work name the overlay instead.
	Rootfs string `json:"rootfs,omitempty"`
	// RootMode is "overlay" or "copy": which shape this workspace's root has. A config from
	// before ADR 0039 has no mode and is a copy, which is what Rootfs says.
	RootMode string `json:"rootMode,omitempty"`
	// Base is the shared image base (the lower), Upper is this workspace's writable layer and Work
	// is overlayfs's own scratch dir. They are set only in overlay mode.
	Base     string   `json:"base,omitempty"`
	Upper    string   `json:"upper,omitempty"`
	Work     string   `json:"work,omitempty"`
	DataDir  string   `json:"home"`
	Hidden   string   `json:"hidden"`
	ReadOnly []string `json:"readOnly,omitempty"`
	// ResolvConf is the file bound at /etc/resolv.conf: the machine's own, or a copy of its
	// upstreams when the machine's points at a loopback resolver the sandbox cannot reach.
	ResolvConf string `json:"resolvConf,omitempty"`
	// Supervisor is this binary, bound read-only into the sandbox: it runs there as the sandbox's
	// first process and holds the namespaces open. Socket is where a worker finds it, and
	// GuestSocket is the same socket as the supervisor sees it. Run and GuestRun are the folder
	// the socket lives in, on both sides of the bind.
	Bwrap       string `json:"bwrap"`
	Supervisor  string `json:"supervisor"`
	Socket      string `json:"socket"`
	GuestSocket string `json:"guestSocket"`
	Run         string `json:"run"`
	GuestRun    string `json:"guestRun"`
	// IdleTimeout stops a sandbox nothing has run in for a while. It is the same policy value the
	// microVM engine is given, so both engines answer the question the same way.
	IdleTimeout string `json:"idleTimeout"`
	// SecretProxy is where this sandbox's commands send a placeholder: the credential for
	// HTTPS_PROXY, the trust bundle, and the port pasta forwards. Absent when the workspace has
	// no secrets, and then the argv forwards nothing.
	SecretProxy *sandbox.SandboxSecretProxy `json:"secretProxy,omitempty"`
	// Devices is whether this sandbox received the host's /dev/net/tun and /dev/kvm.
	Devices bool `json:"devices,omitempty"`
	// Nested is whether this sandbox was created inside another bwrap workspace. A nested
	// sandbox reuses pasta's user namespace instead of trying to make one more inside it, when
	// the parent can make namespaces at all.
	Nested bool `json:"nested,omitempty"`
	// OwnUserNamespace is set when the sandbox is nested but its parent cannot make the
	// namespaces bubblewrap needs: a youki container holds CAP_SETFCAP and not CAP_SYS_ADMIN.
	// The sandbox is still nested; only where its user namespace comes from changes.
	OwnUserNamespace bool `json:"ownUserNamespace,omitempty"`
	// DisableNesting is whether this sandbox must not nest at all. A sandbox with a user
	// namespace of its own is told to refuse another one inside it, which is what the policy
	// that disables nesting means. A nested sandbox that reuses its parent's namespace cannot
	// carry it: it has no user namespace of its own to disable, and its parent's policy is the
	// one that decided.
	DisableNesting bool `json:"disableNesting,omitempty"`
	// Beta notes what this shape does not do yet, so the file does not read as a promise.
	Beta string `json:"beta"`
}

// sandboxProcessEnv is the environment the sandbox's own processes are started with. Three
// variables: a path for the few programs those processes may run, the workspace's private home so a
// tool that wants a cache does not write to the machine's, and a locale so a command's bytes are
// UTF-8 before the supervisor gives it its own environment. Everything else the gateway holds — a
// token, a cloud key — stays out.
func sandboxProcessEnv(config bwrapConfig) []string {
	home := config.DataDir
	if home == "" {
		home = "/"
	}
	return []string{"PATH=" + sandbox.ProcessPath, "HOME=" + home, "LANG=C.UTF-8"}
}

// Create resolves the policy into a config and the workspace's images, then boots it: control's
// policy is "ensure created, then ensure running", and a sandbox that is created but not running
// is a workspace whose tools do not work.
//
// It is idempotent: creating twice rewrites the same config, which is what makes a changed policy
// apply at the next sandbox rather than to a copy nobody updates.
func (b *Bwrap) Create(ctx context.Context, policy sandbox.Policy, dir string) error {
	if err := b.Prepare(ctx, policy, dir); err != nil {
		return err
	}
	return b.Start(ctx, sandbox.Name(policy.Workspace), policy.Secrets)
}

// Prepare writes the config the next start will read, without starting, stopping, or
// recreating anything. Produce calls it for an existing sandbox so an upgrade that changes
// a baked setting (the device grant, the user namespace) is written into the next start;
// the running guest keeps the shape it booted with.
func (b *Bwrap) Prepare(_ context.Context, policy sandbox.Policy, dir string) error {
	_, err := b.writeConfig(policy, dir)
	return err
}

// RestartRequired reports whether the running sandbox's argv already carries the secrets proxy
// this policy needs. pasta's forward and the CA bundle are both baked into that argv, so a
// sandbox that booted without them cannot be given them by refreshing the config file: every
// command would be handed a proxy URL that nothing forwards, which reads as connection refused.
func (b *Bwrap) RestartRequired(_ context.Context, policy sandbox.Policy) (bool, error) {
	running, err := b.readConfig(sandbox.Name(policy.Workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return sandbox.ProxyNeedsRestart(running.SecretProxy, policy.SecretProxy), nil
}

// writeConfig is Create without the boot: what the workspace runs under, written where a person
// can read it and the sandbox can be started from it later.
// validatePolicy refuses a policy before anything is created for it.
func (b *Bwrap) validatePolicy(policy sandbox.Policy) error { return sandbox.ValidateWorkspace(policy) }

// supervisorPaths is everything the supervisor needs to exist before it can run: this binary to
// be, bubblewrap on the machine, and sockets short enough for a Unix path.
type supervisorPaths struct {
	executable string
	bwrap      string
	run        string
	socket     string
}

func (b *Bwrap) supervisor(policy sandbox.Policy) (supervisorPaths, error) {
	executable, err := os.Executable()
	if err != nil {
		return supervisorPaths{}, fmt.Errorf("sandbox: cannot find this binary to run the sandbox's supervisor: %w", err)
	}
	bwrapPath, err := b.look("bwrap")
	if err != nil {
		return supervisorPaths{}, fmt.Errorf("%w: bubblewrap is not installed (install it: apt install bubblewrap)", sandbox.ErrUnavailable)
	}
	run, socket, err := sandbox.SandboxSocketPaths(b.dataDir, policy.Workspace)
	if err != nil {
		return supervisorPaths{}, err
	}
	return supervisorPaths{executable: executable, bwrap: bwrapPath, run: run, socket: socket}, nil
}

// stateDirs makes the two folders every bwrap sandbox needs: the store, and this workspace's
// home inside it.
func (b *Bwrap) stateDirs(policy sandbox.Policy) (string, error) {
	if err := os.MkdirAll(b.state, 0o700); err != nil {
		return "", err
	}
	dataDir := b.workspaceHome(sandbox.Name(policy.Workspace))
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", err
	}
	return dataDir, nil
}

// networkFor is the namespace's networking: off, or pasta carrying it. Egress without pasta is an
// install that needs one `apt install passt` and would otherwise be a sandbox that cannot reach
// anything while claiming it can.
func (b *Bwrap) networkFor(policy sandbox.Policy) (sandbox.SandboxNetwork, error) {
	if !policy.Egress {
		return sandbox.SandboxNetwork{Mode: sandbox.NetworkOffName}, nil
	}
	pasta, err := b.look("pasta")
	if err != nil {
		return sandbox.SandboxNetwork{}, fmt.Errorf("%w: egress in a bwrap workspace is carried by pasta (install it: apt install passt)", sandbox.ErrUnavailable)
	}
	return sandbox.SandboxNetwork{
		Mode: "pasta", Address: sandbox.SandboxAddress, Gateway: sandbox.SandboxGateway, Pasta: pasta,
		Private: policy.PrivateEgress,
	}, nil
}

// network is how this sandbox reaches the network and which resolver it is given. One call, because
// the resolver depends on the network: a workspace with no egress resolves through the machine's
// own file so that it is refused before it is asked, and one with egress resolves through pasta.
func (b *Bwrap) network(policy sandbox.Policy) (sandbox.SandboxNetwork, string, error) {
	network, err := b.networkFor(policy)
	if err != nil {
		return sandbox.SandboxNetwork{}, "", err
	}
	resolvConf, err := sandbox.ResolveResolvFor(b.workspaceDir(sandbox.Name(policy.Workspace)), b.resolver, network.Mode, network.Private)
	if err != nil {
		return sandbox.SandboxNetwork{}, "", err
	}
	return network, resolvConf, nil
}

func (b *Bwrap) writeConfig(policy sandbox.Policy, dir string) (bwrapConfig, error) {
	if err := b.validatePolicy(policy); err != nil {
		return bwrapConfig{}, err
	}
	image := policy.Image
	if strings.TrimSpace(image) == "" {
		image = sandbox.DefaultImage
	}
	dataDir, err := b.stateDirs(policy)
	if err != nil {
		return bwrapConfig{}, err
	}
	name := sandbox.Name(policy.Workspace)
	root, err := sandbox.WorkspaceRoot(policy, b.StateDir(), name, image, b.pull, b.overlayEnabled(image))
	if err != nil {
		return bwrapConfig{}, err
	}
	network, resolvConf, err := b.network(policy)
	if err != nil {
		return bwrapConfig{}, err
	}
	supervisor, err := b.supervisor(policy)
	if err != nil {
		return bwrapConfig{}, err
	}
	secretProxy, err := sandbox.SecretProxyFor(&policy, b.workspaceDir(name), root.ImageRootsPath())
	if err != nil {
		return bwrapConfig{}, err
	}
	nested := sandbox.NestedFromProcess()
	config := bwrapConfig{
		Engine:      b.Name(),
		Workspace:   policy.Workspace,
		Image:       image,
		Network:     network,
		Rootfs:      root.Rootfs,
		RootMode:    root.Mode,
		Base:        root.Base,
		Upper:       root.Upper,
		Work:        root.Work,
		DataDir:     dataDir,
		Hidden:      b.dataDir,
		ReadOnly:    sandbox.ReadonlyGlobals(policy),
		ResolvConf:  resolvConf,
		Supervisor:  supervisor.executable,
		Socket:      supervisor.socket,
		GuestSocket: sandbox.GuestSocketPath,
		Bwrap:       supervisor.bwrap,
		Run:         supervisor.run,
		GuestRun:    sandbox.GuestRunDir,
		IdleTimeout: sandbox.IdleTimeoutOrDefault(policy.IdleTimeout),
		SecretProxy: secretProxy,
		Devices:     policy.Devices,
		Nested:      nested,
		// A pasta parent can make the namespaces a nested sandbox needs; a youki container holds
		// CAP_SETFCAP and not CAP_SYS_ADMIN, so the sandbox asks for one of its own.
		OwnUserNamespace: ownUserNamespace(nested),
		// A workspace that disables VM nesting means a leaf: no hypervisor, no container, no
		// user namespace. The argv enforces it where the sandbox has a namespace of its own.
		DisableNesting: policy.DisableVMNesting,
		Beta:           "one sandbox per workspace, held by this binary as its first process",
	}
	// A directory the workspace may be created under, kept for the person who reads it.
	_ = dir
	return config, b.saveConfig(sandbox.Name(policy.Workspace), config)
}

// saveConfig writes one workspace's record, whole and 0600: a half-written file is a sandbox
// nobody can start.
func (b *Bwrap) saveConfig(name string, config bwrapConfig) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(b.configPath(name), append(data, '\n'), 0o600)
}

// Start boots the workspace's sandbox: one bubblewrap holding the mounts, with this binary as its
// first process inside, and pasta in front of it when the workspace has egress.
//
// The sandbox is a real process with a lifetime, not a view per command. That is what makes a
// daemon outlive the command that started it, what lets pasta's setup be paid once, and what
// Stop has something to stop.
func (b *Bwrap) Start(ctx context.Context, name string, _ []sandbox.Secret) error {
	config, err := b.readConfig(name)
	if err != nil {
		return err
	}
	if b.answers(config.Socket) {
		return nil
	}
	// Decided again here, not only when the sandbox was created: a sandbox made by an earlier
	// build has no grant and points at the machine's resolver, which its network now refuses.
	resolvConf, err := sandbox.ResolveResolvFor(b.workspaceDir(name), b.resolver, config.Network.Mode, config.Network.Private)
	if err != nil {
		return err
	}
	config.ResolvConf = resolvConf
	argv, err := b.bootArgv(config)
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(b.workspaceDir(name), "supervisor.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	command := exec.Command(argv[0], argv[1:]...)
	// Its own environment, and only that: the sandbox's first process reads flags. What it inherited
	// instead was everything acornd was started with, readable from inside at
	// /proc/<pid>/environ — a workspace is not a place to keep the machine's credentials.
	command.Env = sandboxProcessEnv(config)
	// Its own session: the sandbox outlives the gateway that started it, so that a restart can
	// adopt it instead of killing what the workspace was running.
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		return fmt.Errorf("sandbox: starting the supervisor: %w", err)
	}
	if err := os.WriteFile(b.pidPath(name), []byte(strconv.Itoa(command.Process.Pid)), 0o600); err != nil {
		return err
	}
	// Reaped here so it does not become a zombie; the supervisor is the sandbox's business after
	// this, not this process's.
	go func() { _ = command.Wait() }()
	if err := supervise.WaitForReady(ctx, config.Socket, 15*time.Second); err != nil {
		detail := b.logTail(name)
		_ = b.Stop(context.Background(), name)
		if detail != "" {
			return fmt.Errorf("%w: %w: %s", sandbox.ErrUnavailable, err, detail)
		}
		return fmt.Errorf("%w: %w", sandbox.ErrUnavailable, err)
	}
	return nil
}

// Stop ends the sandbox: the supervisor is asked to shut down, which kills every command with
// it, and the files stay. A workspace's disk is not what a stop is for.
func (b *Bwrap) Stop(ctx context.Context, name string) error {
	config, err := b.readConfig(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if b.answers(config.Socket) {
		b.shutdown(config.Socket)
	}
	// Whatever it answered, nothing of it may be left running.
	b.stopProcess(name)
	_ = os.Remove(b.pidPath(name))
	_ = os.Remove(config.Socket)
	return nil
}

// stopProcess ends whatever is still running, asking first: the supervisor has sandboxes and
// sockets to close, and a kill leaves them behind.
func (b *Bwrap) stopProcess(name string) {
	pid := b.pid(name)
	if pid <= 0 {
		return
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = process.Signal(syscall.SIGTERM)
	for range 100 {
		if !sandbox.Alive(pid) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sandbox.Alive(pid) {
		_ = process.Kill()
	}
}

// List reports the sandboxes this engine knows, running or stopped: one per workspace that has
// been created, which is what makes a stopped sandbox startable rather than remade.
func (b *Bwrap) List(context.Context) ([]sandbox.Instance, error) {
	entries, err := os.ReadDir(b.StateDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	instances := make([]sandbox.Instance, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		config, err := b.readConfig(name)
		if err != nil {
			continue
		}
		status := "stopped"
		if b.answers(config.Socket) {
			status = "running"
		}
		instances = append(instances, sandbox.Instance{Name: name, Image: config.Image, Status: status})
	}
	return instances, nil
}

// Remove deletes the config and the workspace's private home — the things a bwrap sandbox
// accumulates — and the socket's folder, which is not in the workspace's folder any more. The
// workspace folder is the user's and is not touched.
func (b *Bwrap) Remove(ctx context.Context, name string) error {
	// Removing a running sandbox removes its files but leaves the supervisor, its bubblewrap,
	// and every command they hold; a test that called Remove after Start leaked them. Stop is
	// idempotent, so the control path that already closed the sandbox pays nothing for it.
	_ = b.Stop(ctx, name)
	// Read before removing: the config is what names the socket's folder, and it lives outside
	// everything else this deletes.
	run := ""
	if config, err := b.readConfig(name); err == nil {
		run = config.Run
	}
	if err := os.Remove(b.configPath(name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if run != "" {
		_ = os.RemoveAll(run)
	}
	return os.RemoveAll(b.workspaceDir(name))
}

// workspaceDir is a path inside this workspace's sandbox folder.
func (b *Bwrap) workspaceDir(name string) string {
	return filepath.Join(b.StateDir(), name)
}

// SecretDelivery is substitution: the guest holds a placeholder, and the proxy this install runs
// for the workspace swaps in the value for the hosts the secret names. See
// docs/adr/0031-a-proxy-for-bwrap-secrets.md.
func (b *Bwrap) SecretDelivery() sandbox.Delivery { return sandbox.DeliverySubstitute }

// SecretProxyInControl is true: the proxy control starts for this workspace is the thing that
// substitutes, so the policy carries its address and its CA.
func (b *Bwrap) SecretProxyInControl() bool { return true }

// SecretEnvInWorker is false: the worker never receives a value, so nothing it spawns — a command,
// a hook, a crash report — can inherit one.
func (b *Bwrap) SecretEnvInWorker() bool { return false }

// SecretsChanged needs no restart. The grants live in the proxy control started for this
// workspace, and it swaps them under the running sandbox; a workspace with no proxy has no values
// to change either.
func (b *Bwrap) SecretsChanged(context.Context, string, []sandbox.Secret) (sandbox.SecretChange, error) {
	return sandbox.SecretChange{}, nil
}

// configPath is where one workspace's config lives, named as the engine names sandboxes so a
// reader can match one to the other.
func (b *Bwrap) configPath(name string) string {
	return filepath.Join(b.state, name+".json")
}

// workspaceHome is the private home a sandbox's commands get: language toolchains install
// there, the host's own home is not written, and removing the sandbox removes it.
func (b *Bwrap) workspaceHome(name string) string {
	return filepath.Join(b.workspaceDir(name), "home")
}

// sandbox.NetworkOffName is how a policy's egress switch is written into the engine's config.

// probeArgs is the smallest sandbox that proves the machine will make one: a user namespace
// and a mount namespace, then a no-op. It touches nothing.
func probeArgs() []string {
	return []string{"--ro-bind", "/", "/", "--unshare-all", "--", "/bin/true"}
}

// lastWord is the version out of "bubblewrap 0.11.0".
func lastWord(out string) string {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// readConfig is the config control wrote, which is also what a person can read.
func (b *Bwrap) readConfig(name string) (bwrapConfig, error) {
	raw, err := os.ReadFile(b.configPath(name))
	if err != nil {
		return bwrapConfig{}, err
	}
	var config bwrapConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return bwrapConfig{}, fmt.Errorf("sandbox: the config for %s is not readable: %w", name, err)
	}
	return config, nil
}

// answers reports whether a supervisor is listening and willing: a socket file left by a dead
// supervisor is not a running sandbox.
func (b *Bwrap) answers(socket string) bool {
	if socket == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return supervise.WaitForReady(ctx, socket, 400*time.Millisecond) == nil
}

// shutdown asks the supervisor to end, which takes every command with it.
func (b *Bwrap) shutdown(socket string) {
	connection, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return
	}
	defer connection.Close()
	_ = json.NewEncoder(connection).Encode(supervise.Request{Type: "shutdown"})
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	var event supervise.Event
	_ = json.NewDecoder(connection).Decode(&event)
}

// bootArgv is the command that brings the sandbox up: pasta in front when the workspace has
// egress, then bubblewrap with the mounts, then this binary as the sandbox's first process.
func (b *Bwrap) bootArgv(config bwrapConfig) ([]string, error) {
	bwrapArgv, err := b.sandboxArgv(config)
	if err != nil {
		return nil, err
	}
	if config.Network.Mode != "pasta" {
		return bwrapArgv, nil
	}
	address, prefix, ok := strings.Cut(config.Network.Address, "/")
	if !ok || config.Network.Pasta == "" || config.Supervisor == "" {
		return nil, errors.New("sandbox: this workspace has egress but its config has no network to carry it: recreate the sandbox")
	}
	// pasta makes the namespace and the tap and configures neither, so the step it runs first
	// is ours: it sets the address and the default route over netlink, refuses the ranges the
	// workspace was not granted, then execs bubblewrap. Nothing here needs iproute2.
	configure := []string{
		config.Supervisor, "internal", "netns",
		"--address", config.Network.Address,
		"--gateway", config.Network.Gateway,
	}
	if !config.Network.Private {
		// A route that says "unreachable" is the whole boundary: the packet never leaves the
		// sandbox, so nothing on the machine or its overlay is dialled. The connected route to
		// the gateway's /24 is more specific and stays, which is what DNS and the sandbox's own
		// address need.
		configure = append(configure, sandbox.RefusePrivate)
	}
	// One port into the sandbox's own loopback: the secrets proxy, and nothing else of the
	// machine. Measured: with -T naming a port, that port answers and every other listener on the
	// machine's loopback stays refused. See docs/adr/0031-a-proxy-for-bwrap-secrets.md.
	forwards := sandbox.NoForwarding
	if config.SecretProxy != nil {
		forwards = strconv.Itoa(config.SecretProxy.Port)
	}
	argv := []string{
		config.Network.Pasta, "-q",
		// Outbound only. pasta's port forwarding defaults to "auto", which mirrors every service
		// the machine listens on into the sandbox's loopback — the acornd API on 7777 and sshd
		// on 22 among them — and that is the machine's own surface, not the workspace's. Nothing in
		// the sandbox needs a port opened for it, and nothing here is a port the machine should
		// offer into a workspace.
		"-t", "none", "-u", "none", "-T", forwards, "-U", "none",
		// The gateway address would otherwise be translated to the machine's loopback, which is
		// the same surface by another name. See docs/adr/0023-bwrap-engine.md.
		"--no-map-gw",
	}
	if !config.Network.Private {
		// The sandbox's resolver is pasta itself, so DNS answers without a route into the
		// machine's networks. See resolveResolv.
		argv = append(argv, "--dns-forward", sandbox.SandboxDNS)
	}
	argv = append(argv, "-a", address, "-n", prefix, "-g", config.Network.Gateway, "--")
	argv = append(argv, configure...)
	return append(argv, bwrapArgv...), nil
}

// deviceArgs gives a sandbox the host devices and the capabilities nested engines need. A
// path that is not there is skipped, not refused: a workspace still opens on a machine
// without a hypervisor. The capabilities are what lets nested pasta write its user namespace
// inside the parent sandbox; binding the devices alone is not enough.
func (b *Bwrap) deviceArgs() []string {
	var args []string
	for _, device := range []string{b.devices.Tun, b.devices.KVM} {
		if device == "" {
			continue
		}
		if info, err := os.Stat(device); err != nil || info.IsDir() {
			continue
		}
		args = append(args, "--dev-bind", device, device)
	}
	if len(args) == 0 {
		return nil
	}
	return append(args,
		"--cap-add", "ALL",
		"--setenv", sandbox.NestedEnv, "1",
	)
}

// sandboxArgv is the bubblewrap command: what a command in this workspace can see, once, for as
// long as the sandbox lives.
func (b *Bwrap) sandboxArgv(config bwrapConfig) ([]string, error) {
	if config.Bwrap == "" {
		return nil, errors.New("sandbox: the config has no bubblewrap path: recreate the sandbox")
	}
	// --clearenv first: bubblewrap applies these in order, and the devices this sandbox was granted
	// add --setenv flags of their own. Clearing after them would clear what they set.
	argv := []string{config.Bwrap, "--clearenv"}
	argv = append(argv, b.mountArgs(config)...)
	argv = append(argv, isolationArgs(config)...)
	argv = append(argv, "--", sandbox.GuestSupervisor, "internal", "sandbox-supervisor", "--socket", config.GuestSocket)
	if config.IdleTimeout != "" {
		// A kept-sandbox.Alive workspace is given a timeout that never fires rather than none at all, so
		// the flag always says what the policy decided.
		argv = append(argv, "--idle-timeout", config.IdleTimeout)
	}
	return argv, nil
}

// hasRoot reports whether this sandbox gets an image root, either a copy or an overlay. It is
// the question the uid mapping and the guest home are about; the machine's own root is the other
// answer.
func (config bwrapConfig) hasRoot() bool {
	return config.Rootfs != "" || config.RootMode == "overlay"
}

// mountArgs is everything the sandbox can see: the root, the devices it was granted, the
// workspace, its private home, the resource folders, and the supervisor.
func (b *Bwrap) mountArgs(config bwrapConfig) []string {
	var argv []string
	if config.RootMode == "overlay" {
		// The base is read-only in practice and shared by every workspace on this image; the
		// upper is what an install touches and lives beside this workspace's home. ADR 0039.
		argv = append(argv, "--overlay-src", config.Base, "--overlay", config.Upper, config.Work, "/",
			"--ro-bind", resolverOrHost(config), "/etc/resolv.conf")
	} else if config.Rootfs != "" {
		argv = append(argv, "--bind", config.Rootfs, "/",
			// A root of its own still needs the machine's resolver, or its upstreams.
			"--ro-bind", resolverOrHost(config), "/etc/resolv.conf")
	} else {
		argv = append(argv, "--ro-bind", "/", "/", "--tmpfs", config.Hidden)
	}
	argv = append(argv, "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp")
	if config.Devices {
		// After --dev, so the bind lands in the /dev this sandbox actually has.
		argv = append(argv, b.deviceArgs()...)
	}
	for _, path := range config.ReadOnly {
		argv = append(argv, "--ro-bind", path, path)
	}
	if config.SecretProxy != nil {
		// After the root, so the bind lands in the image the sandbox actually has. Read-only: the
		// bundle is the install's, and a workspace that could write it could trust itself.
		argv = append(argv, "--ro-bind", config.SecretProxy.BundleFile, sandbox.GuestCABundle)
	}
	return append(argv,
		"--bind", config.DataDir, guestHome(config),
		"--bind", config.Workspace, config.Workspace,
		"--bind", config.Run, config.GuestRun,
		"--ro-bind", config.Supervisor, sandbox.GuestSupervisor)
}

// ownUserNamespace says whether a nested sandbox must ask bubblewrap for a user namespace of its
// own: a parent with CAP_SYS_ADMIN can make the namespaces a nested sandbox needs, and one with
// only CAP_SETFCAP cannot.
func ownUserNamespace(nested bool) bool { return nested && !holdsSysAdmin() }

// isolationArgs is what the sandbox is separated from. A nested sandbox whose parent can make
// namespaces shares the user namespace it was made in, because asking bubblewrap for a third one
// inside it cannot write its uid map: a parent that made a namespace for pasta holds CAP_SYS_ADMIN,
// CAP_NET_ADMIN and CAP_NET_BIND_SERVICE and no CAP_SETUID, so the uid map write is refused while
// the namespaces that still separate the command work. That is true with and without a network, so
// this case never asks for a user namespace; "off" unshares the network, which is an empty
// namespace rather than the parent's egress. A sandbox outside a parent, and a nested one whose
// parent holds only CAP_SETFCAP (a youki container), keeps a user namespace of its own.
func isolationArgs(config bwrapConfig) []string {
	if config.Nested && !config.OwnUserNamespace {
		argv := []string{"--unshare-pid", "--unshare-uts", "--unshare-ipc", "--unshare-cgroup"}
		if config.Network.Mode == sandbox.NetworkOffName {
			return append(argv, "--unshare-net")
		}
		return append(argv, "--share-net")
	}
	var argv []string
	if config.hasRoot() {
		// Root inside a single mapping, so files written by root land owned by the user.
		argv = append(argv, "--uid", "0", "--gid", "0")
	}
	// A user namespace of the sandbox's own, with the single mapping that makes root inside the
	// user outside. It nests inside the one pasta makes when there is egress, which is worth
	// knowing: on a host that restricts unprivileged user namespaces (Ubuntu 24.04 and its
	// AppArmor gate) that second one is what gets refused. See the note in ADR 0023.
	argv = append(argv, "--unshare-all")
	if config.DisableNesting {
		// --disable-userns is refused without an explicit --unshare-user: it checks the flag,
		// not the --unshare-all bundle. --assert-userns-disabled turns a kernel that cannot
		// install the filter into a failed boot, so the promise is kept or the sandbox does
		// not exist. A nested sandbox that reuses its parent's namespace has no namespace of
		// its own to disable, and bubblewrap refuses the flag without one.
		argv = append(argv, "--unshare-user", "--disable-userns", "--assert-userns-disabled")
	}
	if config.Network.Mode != sandbox.NetworkOffName {
		// Share the network namespace of whoever started bubblewrap: pasta, when there is one.
		argv = append(argv, "--share-net")
	}
	return argv
}

// guestHome is where the private home appears inside the sandbox: the root's own home when there
// is an image, and its real path when the machine's root is the root.
func guestHome(config bwrapConfig) string {
	if !config.hasRoot() {
		return config.DataDir
	}
	return "/root"
}

// pidPath is where the sandbox's process is recorded, so a gateway that restarts can tell a live
// sandbox from a socket file left behind by a dead one.
func (b *Bwrap) pidPath(name string) string {
	return filepath.Join(b.workspaceDir(name), "supervisor.pid")
}

// pid is the sandbox's recorded process, or 0.
func (b *Bwrap) pid(name string) int {
	raw, err := os.ReadFile(b.pidPath(name))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	return pid
}

// logTail is the supervisor's last words, for a start that failed.
func (b *Bwrap) logTail(name string) string {
	raw, err := os.ReadFile(filepath.Join(b.workspaceDir(name), "supervisor.log"))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, "; ")
}

// resolverOrHost is the file a sandbox reads for DNS.
func resolverOrHost(config bwrapConfig) string {
	if config.ResolvConf != "" {
		return config.ResolvConf
	}
	return "/etc/resolv.conf"
}
