package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/supervise"
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
	runner Runner
	look   func(string) (string, error)
	// pull fetches an image into a directory. It is a field so a test can answer without a
	// registry, the same way the runner answers without a binary.
	pull imageSource
	// resolver is the machine's resolver file and the file its upstream servers are in. Paths are
	// fields so a test can answer for a machine that is not this one.
	resolver resolverFiles
	// pasta is the egress probe, a field so a test can answer without running one.
	pasta func(string) error
	// devices are the host paths a sandbox may be given. They are fields so a test can
	// answer for a machine that has neither without making the suite depend on this one.
	devices hostDevices

	once    sync.Once
	version string
}

// hostDevices is where the host's tun and KVM nodes are. A path that is empty, missing, or
// not a device is simply not bound: a workspace still opens, and the engine says why it
// cannot nest instead of failing the boot.
type hostDevices struct {
	tun string
	kvm string
}

// nestedEnv marks a process that is already inside a sandbox. The outer bwrap sets it for
// the workspace's processes; a bwrap engine that finds it reuses the user namespace pasta
// made instead of creating a third one inside it.
const nestedEnv = "ACORNFALL_NESTED"

// NewBwrap builds the driver. Unlike the microVM engine it installs nothing: bubblewrap is a
// distribution package, and installing one is the operator's business, not a gateway's.
func NewBwrap(dataDir string) *Bwrap {
	return &Bwrap{
		dataDir:  dataDir,
		state:    filepath.Join(dataDir, "sandboxes", "bwrap"),
		look:     exec.LookPath,
		pull:     pullImage,
		resolver: resolverFiles{Host: "/etc/resolv.conf", Upstream: "/run/systemd/resolve/resolv.conf"},
		devices:  hostDevices{tun: "/dev/net/tun", kvm: "/dev/kvm"},
	}
}

// WithDevicePaths answers "does this machine have them" for a test, which must not depend on
// the device nodes of the machine running it.
func (b *Bwrap) WithDevicePaths(tun, kvm string) *Bwrap {
	b.devices = hostDevices{tun: tun, kvm: kvm}
	return b
}

// WithImageSource replaces the registry with a directory of its own, which is how the pull and
// the unpack are tested without a network.
func (b *Bwrap) WithImageSource(pull imageSource) *Bwrap {
	b.pull = pull
	return b
}

// WithRunner replaces the bubblewrap binary, which is how the driver is tested without one.
func (b *Bwrap) WithRunner(runner Runner) *Bwrap {
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
		return text, fmt.Errorf("bubblewrap: %s", firstLine(text))
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
func (b *Bwrap) Env() []string { return []string{"ACORNFALL_BWRAP_STATE=" + b.state} }

func (b *Bwrap) Installed() bool {
	_, err := b.look("bwrap")
	return err == nil
}

// Install cannot install: bubblewrap is a package, and apt needs root. The refusal names the
// command, because "sandbox unavailable" with no next step is not an answer.
func (b *Bwrap) Install(context.Context, func(string, ...any)) error {
	if b.Installed() {
		return nil
	}
	return fmt.Errorf("%w: bubblewrap is not installed (install it: apt install bubblewrap)", ErrUnavailable)
}

// Usable is a real probe: a user namespace is the one thing a bwrap sandbox cannot do without,
// and Ubuntu 24.04+ refuses unprivileged ones through AppArmor. The probe is one millisecond
// process, and it answers the question the machine will answer later anyway.
func (b *Bwrap) Usable() error {
	if !b.Installed() {
		return fmt.Errorf("%w: bubblewrap is not installed (install it: apt install bubblewrap)", ErrUnavailable)
	}
	if out, err := b.run(context.Background(), probeArgs()...); err != nil {
		detail := strings.TrimSpace(out)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("%w: bubblewrap cannot create a user namespace here: %s (Ubuntu 23.10 and later restrict them with AppArmor; fix: sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0)", ErrUnavailable, lastLine(detail))
	}
	return nil
}

// Egress reports whether a workspace with its network on can run here: pasta carries it, and
// that means pasta can make a namespace on this machine. A machine without pasta can still run a
// workspace with its network off, which is why this is not Usable.
func (b *Bwrap) Egress() error {
	pasta, err := b.look("pasta")
	if err != nil {
		return fmt.Errorf("%w: a workspace with its network on needs pasta to carry it (install it: apt install passt)", ErrUnavailable)
	}
	if b.pasta != nil {
		return b.pasta(pasta)
	}
	return probePasta(pasta)
}

// probePasta is a real probe, not a version check: pasta makes the namespace before it carries
// anything, and a machine that refuses that refuses every network-on workspace at start. It runs
// one cheap command there, which is what a start does anyway.
func probePasta(pasta string) error {
	return probePastaWithin(pasta, 10*time.Second)
}

// probePastaWithin is probePasta with the wait it is allowed, so a test can ask the question in a
// second instead of ten. The wait is the context's; WaitDelay bounds the other half — pasta can
// leave a process holding the output pipe, and CombinedOutput waits for every writer to close it.
// A probe that waits forever is a sandbox that never starts, which is what a pasta stuck on a
// missing /dev/net/tun produced inside a dev workspace.
func probePastaWithin(pasta string, wait time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	command := exec.CommandContext(ctx, pasta,
		"-q", "-t", "none", "-u", "none", "-T", "none", "-U", "none", "--no-map-gw",
		"-a", "10.0.2.15", "-n", "24", "-g", "10.0.2.2", "--", "/bin/true")
	command.WaitDelay = time.Second
	output, err := command.CombinedOutput()
	detail := strings.TrimSpace(string(output))
	// pasta can print a failure on stderr and still exit 0: inside a bwrap workspace without
	// the caps nested pasta needs, it cannot write its uid map and runs the command anyway.
	// The caller must not read that as a network that works.
	if err == nil && !pastaUserNamespaceFailure(detail) {
		return nil
	}
	if detail == "" && err != nil {
		detail = err.Error()
	}
	return fmt.Errorf("%w: pasta cannot make a network namespace here: %s", ErrUnavailable, lastLine(detail))
}

// pastaUserNamespaceFailure is the failure pasta prints when it cannot create the user
// namespace it runs the sandbox in. The exit status is 0 because the command it ran still
// ran; only the output says the namespace never existed.
func pastaUserNamespaceFailure(detail string) bool {
	return strings.Contains(detail, "Couldn't configure user mappings") ||
		strings.Contains(detail, "Couldn't write to /proc/self/uid_map")
}

// Nesting reports whether a hypervisor can run in a bwrap sandbox. It can when the machine
// has /dev/kvm and the workspace grants devices: the device is bound into the sandbox and
// the capabilities nested pasta needs come with it.
func (b *Bwrap) Nesting(context.Context) NestingSupport {
	if _, err := os.Stat(b.devices.kvm); err != nil {
		return unavailable("no /dev/kvm on this machine, so a hypervisor inside a bwrap sandbox cannot start")
	}
	return NestingSupport{Available: true}
}

// config is what one workspace's commands run under, written for a person to read and for the
// worker to build an argv from.
type bwrapConfig struct {
	Engine    string `json:"engine"`
	Workspace string `json:"workspace"`
	Image     string `json:"image"`
	// Network is how a command reaches the network: a namespace with no interfaces, or one whose
	// traffic pasta forwards through the host. See bwrapNetwork.
	Network bwrapNetwork `json:"network"`
	// Rootfs is the workspace's own unpacked copy of its image, bound as `/`. It is where an
	// install inside the sandbox lands and stays: the host never sees it, and removing the
	// sandbox removes it.
	Rootfs   string   `json:"rootfs,omitempty"`
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
	SecretProxy *bwrapSecretProxy `json:"secretProxy,omitempty"`
	// Devices is whether this sandbox received the host's /dev/net/tun and /dev/kvm.
	Devices bool `json:"devices,omitempty"`
	// Nested is whether this sandbox was created inside another bwrap workspace. A nested
	// sandbox reuses pasta's user namespace instead of trying to make one more inside it.
	Nested bool `json:"nested,omitempty"`
	// Beta notes what this shape does not do yet, so the file does not read as a promise.
	Beta string `json:"beta"`
}

// processPath is what the sandbox's own processes find a program on. The supervisor is started by
// absolute path and reads flags, but a shell inside a sandbox is started with the policy's own PATH;
// this is the one the sandbox process itself gets before anything of the workspace exists.
const processPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

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
	return []string{"PATH=" + processPath, "HOME=" + home, "LANG=C.UTF-8"}
}

// Where the supervisor lives inside a sandbox, and where its socket does. The guest paths are
// short on purpose: a Unix socket address is limited, and the host side of the same socket
// carries the workspace's name.
const (
	guestSupervisor = "/usr/local/bin/acornfall"
	guestRunDir     = "/run/acornfall"
	guestSocketPath = guestRunDir + "/s.sock"
)

// socketLimit is the length a Unix socket path may have (Linux allows 107 characters and a
// terminator). Both sides of one socket have to fit, and the host side is the long one, which is
// why the folder is named by a short hash rather than by the sandbox's whole name.
const socketLimit = 106

// bwrapSecretProxy is what a command needs to use this workspace's secrets: a proxy URL with its
// session credential, the bundle a client has to trust, and the port pasta forwards. The worker
// reads the first two and sets them in the environment it hands each command.
type bwrapSecretProxy struct {
	URL string `json:"url"`
	// BundleFile is the trust bundle on the machine; Bundle is where it is bound inside, and is
	// repeated here because the worker tells node about it.
	BundleFile string `json:"bundleFile"`
	Bundle     string `json:"bundle"`
	Port       int    `json:"port"`
}

// guestCABundle is the path every Debian-family image reads its roots from, and Alpine ships it
// too. The workspace's bundle is bound over it: a client reads one file and finds the image's own
// roots and this install's CA, which is what keeps every other TLS connection working.
const guestCABundle = "/etc/ssl/certs/ca-certificates.crt"

// noForwarding is pasta's word for a port it does not carry. A workspace with no proxy forwards
// nothing, which is the shape every workspace had before the proxy existed.
const noForwarding = "none"

// bwrapNetwork is the network a command gets. The tool paths are resolved when the workspace is
// created, so a missing one is a sentence then rather than a mystery in the middle of a turn.
type bwrapNetwork struct {
	Mode string `json:"mode"`
	// Private is whether this sandbox may reach private networks. False, the default, gives it
	// routes that refuse them: pasta carries the machine's own routing table, so a sandbox whose
	// routes say nothing reaches the machine's LAN and its overlay as the machine does.
	Private bool `json:"private,omitempty"`
	// Address is the sandbox's own address inside that namespace, and Gateway is pasta's address
	// there. Both live inside the sandbox: nothing about them appears on the host.
	Address string `json:"address,omitempty"`
	Gateway string `json:"gateway,omitempty"`
	Pasta   string `json:"pasta,omitempty"`
}

// The sandbox's network, when it has one. It is the address space QEMU's user-mode network has
// used for twenty years, and it is chosen not to collide with the ranges a machine is likely to
// have.
const (
	sandboxAddress = "10.0.2.15/24"
	sandboxGateway = "10.0.2.2"
	// sandboxDNS is where the sandbox sends its queries when its private networks are refused:
	// pasta maps it to the machine's own resolver, so DNS never needs a route into anything
	// private. It is inside 10.0.2.0/24, whose connected route is more specific than the refused
	// ranges, which is what keeps the gateway and this address reachable.
	sandboxDNS = "10.0.2.3"
)

// refusePrivate is the flag that asks the namespace's own configuration to install the routes
// refusing private ranges. The list is not here: it lives where the routes are installed
// (cmd/acornfall/netns.go), so one file decides what a workspace without a grant cannot reach.
const refusePrivate = "--refuse-private"

// Create resolves the policy into a config and the workspace's images, then boots it: control's
// policy is "ensure created, then ensure running", and a sandbox that is created but not running
// is a workspace whose tools do not work.
//
// It is idempotent: creating twice rewrites the same config, which is what makes a changed policy
// apply at the next sandbox rather than to a copy nobody updates.
func (b *Bwrap) Create(ctx context.Context, policy Policy, dir string) error {
	if err := b.Prepare(ctx, policy, dir); err != nil {
		return err
	}
	return b.Start(ctx, Name(policy.Workspace), policy.Secrets)
}

// Prepare writes the config the next start will read, without starting, stopping, or
// recreating anything. Produce calls it for an existing sandbox so an upgrade that changes
// a baked setting (the device grant, the user namespace) is written into the next start;
// the running guest keeps the shape it booted with.
func (b *Bwrap) Prepare(_ context.Context, policy Policy, dir string) error {
	_, err := b.writeConfig(policy, dir)
	return err
}

// writeConfig is Create without the boot: what the workspace runs under, written where a person
// can read it and the sandbox can be started from it later.
// validatePolicy refuses a policy before anything is created for it.
func (b *Bwrap) validatePolicy(policy Policy) error {
	if strings.TrimSpace(policy.Workspace) == "" {
		return errors.New("sandbox: policy has no workspace")
	}
	info, err := os.Stat(policy.Workspace)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("sandbox: no workspace folder at %s", policy.Workspace)
	}
	return nil
}

// supervisorPaths is everything the supervisor needs to exist before it can run: this binary to
// be, bubblewrap on the machine, and sockets short enough for a Unix path.
type supervisorPaths struct {
	executable string
	bwrap      string
	run        string
	socket     string
}

func (b *Bwrap) supervisor(policy Policy) (supervisorPaths, error) {
	executable, err := os.Executable()
	if err != nil {
		return supervisorPaths{}, fmt.Errorf("sandbox: cannot find this binary to run the sandbox's supervisor: %w", err)
	}
	bwrapPath, err := b.look("bwrap")
	if err != nil {
		return supervisorPaths{}, fmt.Errorf("%w: bubblewrap is not installed (install it: apt install bubblewrap)", ErrUnavailable)
	}
	run, socket, err := b.socketPaths(policy)
	if err != nil {
		return supervisorPaths{}, err
	}
	return supervisorPaths{executable: executable, bwrap: bwrapPath, run: run, socket: socket}, nil
}

// stateDirs makes the two folders every bwrap sandbox needs: the store, and this workspace's
// home inside it.
func (b *Bwrap) stateDirs(policy Policy) (string, error) {
	if err := os.MkdirAll(b.state, 0o700); err != nil {
		return "", err
	}
	dataDir := b.workspaceHome(Name(policy.Workspace))
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", err
	}
	return dataDir, nil
}

// readonlyGlobals is the machine's global resources that exist: a path the policy names but this
// machine does not have is a mount that would fail, not a resource to mount.
func readonlyGlobals(policy Policy) []string {
	var readonly []string
	for _, path := range policy.Global {
		if _, err := os.Stat(path); err == nil {
			readonly = append(readonly, path)
		}
	}
	sort.Strings(readonly)
	return readonly
}

// networkFor is the namespace's networking: off, or pasta carrying it. Egress without pasta is an
// install that needs one `apt install passt` and would otherwise be a sandbox that cannot reach
// anything while claiming it can.
func (b *Bwrap) networkFor(policy Policy) (bwrapNetwork, error) {
	if !policy.Egress {
		return bwrapNetwork{Mode: NetworkOffName}, nil
	}
	pasta, err := b.look("pasta")
	if err != nil {
		return bwrapNetwork{}, fmt.Errorf("%w: egress in a bwrap workspace is carried by pasta (install it: apt install passt)", ErrUnavailable)
	}
	return bwrapNetwork{
		Mode: "pasta", Address: sandboxAddress, Gateway: sandboxGateway, Pasta: pasta,
		Private: policy.PrivateEgress,
	}, nil
}

// network is how this sandbox reaches the network and which resolver it is given. One call, because
// the resolver depends on the network: a workspace with no egress resolves through the machine's
// own file so that it is refused before it is asked, and one with egress resolves through pasta.
func (b *Bwrap) network(policy Policy) (bwrapNetwork, string, error) {
	network, err := b.networkFor(policy)
	if err != nil {
		return bwrapNetwork{}, "", err
	}
	resolvConf, err := b.resolveResolv(Name(policy.Workspace), network)
	if err != nil {
		return bwrapNetwork{}, "", err
	}
	return network, resolvConf, nil
}

// socketPaths is the session's runtime folder and the two sockets in it. The socket lives there
// rather than in the store because a Unix socket path has a hard length and this way the length
// is the same wherever the install lives; the limit is checked here because the failure without
// it is a supervisor that never answers.
func (b *Bwrap) socketPaths(policy Policy) (run, socket string, err error) {
	run, err = b.socketDir(policy.Workspace)
	if err != nil {
		return "", "", err
	}
	socket = filepath.Join(run, "s.sock")
	for _, path := range []string{socket, guestSocketPath} {
		if len(path) > socketLimit {
			return "", "", fmt.Errorf("sandbox: the supervisor's socket path is too long for a Unix socket: %s is %d characters and %d is the limit (shorten XDG_RUNTIME_DIR, or the workspace name)", path, len(path), socketLimit)
		}
	}
	return run, socket, nil
}

func (b *Bwrap) writeConfig(policy Policy, dir string) (bwrapConfig, error) {
	if err := b.validatePolicy(policy); err != nil {
		return bwrapConfig{}, err
	}
	image := policy.Image
	if strings.TrimSpace(image) == "" {
		image = DefaultImage
	}
	dataDir, err := b.stateDirs(policy)
	if err != nil {
		return bwrapConfig{}, err
	}
	rootfs, err := b.workspaceRootfs(policy, Name(policy.Workspace), image)
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
	secretProxy, err := b.secretProxyFor(policy, Name(policy.Workspace), rootfs)
	if err != nil {
		return bwrapConfig{}, err
	}
	config := bwrapConfig{
		Engine:      b.Name(),
		Workspace:   policy.Workspace,
		Image:       image,
		Network:     network,
		Rootfs:      rootfs,
		DataDir:     dataDir,
		Hidden:      b.dataDir,
		ReadOnly:    readonlyGlobals(policy),
		ResolvConf:  resolvConf,
		Supervisor:  supervisor.executable,
		Socket:      supervisor.socket,
		GuestSocket: guestSocketPath,
		Bwrap:       supervisor.bwrap,
		Run:         supervisor.run,
		GuestRun:    guestRunDir,
		IdleTimeout: idleTimeoutOrDefault(policy.IdleTimeout),
		SecretProxy: secretProxy,
		Devices:     policy.Devices,
		Nested:      os.Getenv(nestedEnv) == "1",
		Beta:        "one sandbox per workspace, held by this binary as its first process",
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return bwrapConfig{}, err
	}
	// A directory the workspace may be created under, kept for the person who reads it.
	_ = dir
	if err := os.WriteFile(b.configPath(Name(policy.Workspace)), append(data, '\n'), 0o600); err != nil {
		return bwrapConfig{}, err
	}
	return config, nil
}

// Start boots the workspace's sandbox: one bubblewrap holding the mounts, with this binary as its
// first process inside, and pasta in front of it when the workspace has egress.
//
// The sandbox is a real process with a lifetime, not a view per command. That is what makes a
// daemon outlive the command that started it, what lets pasta's setup be paid once, and what
// Stop has something to stop.
func (b *Bwrap) Start(ctx context.Context, name string, _ []Secret) error {
	config, err := b.readConfig(name)
	if err != nil {
		return err
	}
	if b.answers(config.Socket) {
		return nil
	}
	// Decided again here, not only when the sandbox was created: a sandbox made by an earlier
	// build has no grant and points at the machine's resolver, which its network now refuses.
	resolvConf, err := b.resolveResolv(name, config.Network)
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
	// instead was everything acornfall was started with, readable from inside at
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
			return fmt.Errorf("%w: %w: %s", ErrUnavailable, err, detail)
		}
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
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
		if !alive(pid) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if alive(pid) {
		_ = process.Kill()
	}
}

// List reports the sandboxes this engine knows, running or stopped: one per workspace that has
// been created, which is what makes a stopped sandbox startable rather than remade.
func (b *Bwrap) List(context.Context) ([]Instance, error) {
	entries, err := os.ReadDir(b.StateDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	instances := make([]Instance, 0, len(entries))
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
		instances = append(instances, Instance{Name: name, Image: config.Image, Status: status})
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

// workspaceRootfs returns the workspace's own copy of its image, making it on first use. A
// copy rather than a shared read-only tree with a layer on top: an install inside the sandbox
// is then an ordinary write to an ordinary directory, which needs no privileged mount, no
// overlay, and no cleanup beyond removing the folder.
func (b *Bwrap) workspaceRootfs(policy Policy, name, image string) (string, error) {
	rootfs := filepath.Join(b.workspaceDir(name), "rootfs")
	if info, err := os.Stat(filepath.Join(rootfs, ".acornfall-rootfs")); err == nil && !info.IsDir() {
		return rootfs, nil
	}
	// The workspace's own copy is the slow part of a first start — 84 MB of image, more after the
	// project's tools — so the two steps that take the time are named as they happen.
	policy.Stage("preparing the image")
	base, err := b.imageBase(image)
	if err != nil {
		return "", err
	}
	// Replaced whole, so a half-copied tree cannot be mistaken for a finished one.
	policy.Stage("copying the image")
	_ = os.RemoveAll(rootfs)
	if err := copyTree(base, rootfs); err != nil {
		return "", fmt.Errorf("sandbox: copying the image for this workspace: %w", err)
	}
	if err := os.WriteFile(filepath.Join(rootfs, ".acornfall-rootfs"), []byte(image+"\n"), 0o644); err != nil {
		return "", err
	}
	// apt's own sandbox drops to _apt with setgroups, which a single-mapping user namespace
	// denies; as root inside a root that is the workspace's own, dropping is not needed.
	aptDir := filepath.Join(rootfs, "etc", "apt", "apt.conf.d")
	if err := os.MkdirAll(aptDir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(aptDir, "99-acornfall"), []byte("APT::Sandbox::User \"root\";\n"), 0o644)
	}
	return rootfs, nil
}

// imageBase is the shared unpacked image, fetched once per digest and reused by every
// workspace. It is read-only in practice: a workspace copies it before it runs anything.
func (b *Bwrap) imageBase(image string) (string, error) {
	if strings.TrimSpace(image) == "" {
		image = DefaultImage
	}
	ref, err := parseImageReference(image)
	if err != nil {
		return "", fmt.Errorf("sandbox: %w", err)
	}
	key := slugify(ref.host+"-"+ref.repository) + "-" + slugify(ref.tag)
	dir := filepath.Join(b.StateDir(), "images", key)
	base := filepath.Join(dir, "base")
	complete := filepath.Join(dir, "complete")
	if imageReady(complete) {
		return base, nil
	}
	if err := b.pullToDir(dir, base, complete, image); err != nil {
		return "", err
	}
	return base, nil
}

// imageReady is the marker the pull writes last: a base folder without it is a pull that did not
// finish, and one that was interrupted.
func imageReady(complete string) bool {
	info, err := os.Stat(complete)
	return err == nil && !info.IsDir()
}

// pullToDir unpacks an image under the image lock. One pull per image at a time, across
// processes: a workspace copying a half-written image is a failure with no cause in it, and two
// gateways on one machine share this folder. The marker is re-checked after the lock is taken,
// because whoever held it may have finished the job.
func (b *Bwrap) pullToDir(dir, base, complete, image string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return lockFile(filepath.Join(dir, "lock"), func() error {
		if imageReady(complete) {
			return nil
		}
		if b.pull == nil {
			return errors.New("sandbox: no image source is configured")
		}
		staging, err := os.MkdirTemp(dir, "base.partial-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(staging) }()
		if err := b.pull(context.Background(), image, staging, nil); err != nil {
			return fmt.Errorf("sandbox: fetching the image %s: %w", image, err)
		}
		_ = os.RemoveAll(base)
		if err := os.Rename(staging, base); err != nil {
			return err
		}
		return os.WriteFile(complete, []byte(image+"\n"), 0o644)
	})
}

// secretProxyFor assembles what a sandbox needs to trust and reach the workspace's proxy: the port
// pasta forwards, the URL a command puts in HTTPS_PROXY, and a trust bundle built from the image's
// own roots plus this install's CA.
//
// The bundle is the image's roots *and* ours on purpose. Binding only ours would work for a secret
// and break every other TLS connection a command makes, which is a sandbox that cannot install a
// package; binding only the image's is a proxy nobody trusts.
func (b *Bwrap) secretProxyFor(policy Policy, name, rootfs string) (*bwrapSecretProxy, error) {
	if policy.SecretProxy == nil {
		return nil, nil
	}
	addr := policy.SecretProxy.Addr()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("sandbox: the secrets proxy has no usable address (%q): %w", policy.SecretProxy.URL, err)
	}
	number, err := strconv.Atoi(port)
	if err != nil || number <= 0 || number > 65535 {
		return nil, fmt.Errorf("sandbox: the secrets proxy port %q is not a port", port)
	}
	ca, err := os.ReadFile(policy.SecretProxy.CAFile)
	if err != nil {
		return nil, fmt.Errorf("sandbox: reading the secrets proxy's CA: %w", err)
	}
	bundleFile := filepath.Join(b.workspaceDir(name), "ca-bundle.pem")
	if err := writeTrustBundle(bundleFile, imageRoots(rootfs), ca); err != nil {
		return nil, err
	}
	return &bwrapSecretProxy{
		URL:        policy.SecretProxy.URL,
		BundleFile: bundleFile,
		Bundle:     guestCABundle,
		Port:       number,
	}, nil
}

// imageRoots is the roots a sandbox would have trusted before this install's CA was added: its own
// image's bundle, or the machine's when the workspace binds the host root read-only.
func imageRoots(rootfs string) []byte {
	paths := []string{guestCABundle}
	if rootfs != "" {
		paths = append([]string{filepath.Join(rootfs, guestCABundle)}, paths...)
	}
	for _, path := range paths {
		if data, err := os.ReadFile(path); err == nil {
			return data
		}
	}
	return nil
}

// writeTrustBundle writes the image's roots followed by the install's CA, each on its own lines so
// a PEM reader sees two blocks rather than one concatenated blob.
func writeTrustBundle(path string, roots, ca []byte) error {
	bundle := make([]byte, 0, len(roots)+len(ca)+2)
	bundle = appendBlock(bundle, roots)
	bundle = appendBlock(bundle, ca)
	if err := os.WriteFile(path, bundle, 0o644); err != nil {
		return fmt.Errorf("sandbox: writing the trust bundle: %w", err)
	}
	return nil
}

func appendBlock(bundle, block []byte) []byte {
	if len(block) == 0 {
		return bundle
	}
	bundle = append(bundle, block...)
	if !strings.HasSuffix(string(block), "\n") {
		bundle = append(bundle, '\n')
	}
	return bundle
}

// workspaceDir is a path inside this workspace's sandbox folder.
func (b *Bwrap) workspaceDir(name string) string {
	return filepath.Join(b.StateDir(), name)
}

// SecretDelivery is substitution: the guest holds a placeholder, and the proxy this install runs
// for the workspace swaps in the value for the hosts the secret names. See
// docs/adr/0031-a-proxy-for-bwrap-secrets.md.
func (b *Bwrap) SecretDelivery() Delivery { return DeliverySubstitute }

// SecretProxyInControl is true: the proxy control starts for this workspace is the thing that
// substitutes, so the policy carries its address and its CA.
func (b *Bwrap) SecretProxyInControl() bool { return true }

// SecretEnvInWorker is false: the worker never receives a value, so nothing it spawns — a command,
// a hook, a crash report — can inherit one.
func (b *Bwrap) SecretEnvInWorker() bool { return false }

// SecretsChanged needs no restart. The grants live in the proxy control started for this
// workspace, and it swaps them under the running sandbox; a workspace with no proxy has no values
// to change either.
func (b *Bwrap) SecretsChanged(context.Context, string, []Secret) (SecretChange, error) {
	return SecretChange{}, nil
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

// NetworkOffName is how a policy's egress switch is written into the engine's config.
const NetworkOffName = "off"

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

// lastLine keeps a probe's failure to one line, so a state frame stays readable.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
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
	sandbox, err := b.sandboxArgv(config)
	if err != nil {
		return nil, err
	}
	if config.Network.Mode != "pasta" {
		return sandbox, nil
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
		configure = append(configure, refusePrivate)
	}
	// One port into the sandbox's own loopback: the secrets proxy, and nothing else of the
	// machine. Measured: with -T naming a port, that port answers and every other listener on the
	// machine's loopback stays refused. See docs/adr/0031-a-proxy-for-bwrap-secrets.md.
	forwards := noForwarding
	if config.SecretProxy != nil {
		forwards = strconv.Itoa(config.SecretProxy.Port)
	}
	argv := []string{
		config.Network.Pasta, "-q",
		// Outbound only. pasta's port forwarding defaults to "auto", which mirrors every service
		// the machine listens on into the sandbox's loopback — the acornfall API on 7777 and sshd
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
		argv = append(argv, "--dns-forward", sandboxDNS)
	}
	argv = append(argv, "-a", address, "-n", prefix, "-g", config.Network.Gateway, "--")
	argv = append(argv, configure...)
	return append(argv, sandbox...), nil
}

// deviceArgs gives a sandbox the host devices and the capabilities nested engines need. A
// path that is not there is skipped, not refused: a workspace still opens on a machine
// without a hypervisor. The capabilities are what lets nested pasta write its user namespace
// inside the parent sandbox; binding the devices alone is not enough.
func (b *Bwrap) deviceArgs() []string {
	var args []string
	for _, device := range []string{b.devices.tun, b.devices.kvm} {
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
		"--setenv", nestedEnv, "1",
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
	argv = append(argv, "--", guestSupervisor, "internal", "sandbox-supervisor", "--socket", config.GuestSocket)
	if config.IdleTimeout != "" {
		// A kept-alive workspace is given a timeout that never fires rather than none at all, so
		// the flag always says what the policy decided.
		argv = append(argv, "--idle-timeout", config.IdleTimeout)
	}
	return argv, nil
}

// mountArgs is everything the sandbox can see: the root, the devices it was granted, the
// workspace, its private home, the resource folders, and the supervisor.
func (b *Bwrap) mountArgs(config bwrapConfig) []string {
	var argv []string
	if config.Rootfs != "" {
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
		argv = append(argv, "--ro-bind", config.SecretProxy.BundleFile, guestCABundle)
	}
	return append(argv,
		"--bind", config.DataDir, guestHome(config),
		"--bind", config.Workspace, config.Workspace,
		"--bind", config.Run, config.GuestRun,
		"--ro-bind", config.Supervisor, guestSupervisor)
}

// isolationArgs is what the sandbox is separated from. A nested sandbox with a network shares
// the user namespace pasta already made: asking bubblewrap for a third one inside it cannot
// write its uid map, even when the process is root. The namespaces that still separate the
// command (pid, uts, ipc, cgroup) are unshared one by one; the user namespace is pasta's. A
// sandbox without a network, or outside a parent, keeps a user namespace of its own.
func isolationArgs(config bwrapConfig) []string {
	nestedNetwork := config.Nested && config.Network.Mode != NetworkOffName
	var argv []string
	if config.Rootfs != "" && !nestedNetwork {
		// Root inside a single mapping, so files written by root land owned by the user.
		argv = append(argv, "--uid", "0", "--gid", "0")
	}
	if nestedNetwork {
		return append(argv, "--unshare-pid", "--unshare-uts", "--unshare-ipc", "--unshare-cgroup", "--share-net")
	}
	// A user namespace of the sandbox's own, with the single mapping that makes root inside the
	// user outside. It nests inside the one pasta makes when there is egress, which is worth
	// knowing: on a host that restricts unprivileged user namespaces (Ubuntu 24.04 and its
	// AppArmor gate) that second one is what gets refused. See the note in ADR 0023.
	argv = append(argv, "--unshare-all")
	if config.Network.Mode != NetworkOffName {
		// Share the network namespace of whoever started bubblewrap: pasta, when there is one.
		argv = append(argv, "--share-net")
	}
	return argv
}

// guestHome is where the private home appears inside the sandbox: the root's own home when there
// is an image, and its real path when the machine's root is the root.
func guestHome(config bwrapConfig) string {
	if config.Rootfs == "" {
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

// alive reports whether a process exists. Signal 0 asks the question without sending anything.
func alive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
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

// resolverFiles is where a machine keeps its resolver configuration: the file in use, and the one
// its upstream servers are written to when a local resolver owns 127.0.0.53.
type resolverFiles struct {
	Host     string
	Upstream string
}

// resolverOrHost is the file a sandbox reads for DNS.
func resolverOrHost(config bwrapConfig) string {
	if config.ResolvConf != "" {
		return config.ResolvConf
	}
	return "/etc/resolv.conf"
}

// resolveResolv decides what a sandbox should read for DNS.
//
// A workspace granted private networks reads the machine's resolver, because it can reach it. One
// without the grant cannot: the machine's resolver is often an overlay address (Tailscale's
// MagicDNS at 100.100.100.100, systemd-resolved at 127.0.0.53), which is on the list of ranges the
// sandbox refuses. That sandbox reads a file naming pasta, which forwards the query to the machine's
// resolver from the machine's side — the sandbox never needs a route into anything private, and a
// name still resolves.
func (b *Bwrap) resolveResolv(name string, network bwrapNetwork) (string, error) {
	if network.Mode != "pasta" || network.Private {
		return b.resolverFile(b.workspaceDir(name))
	}
	path := filepath.Join(b.workspaceDir(name), "pasta-resolv.conf")
	content := "# written by acornfall: pasta forwards these queries to the machine's resolver\n" +
		"nameserver " + sandboxDNS + "\n"
	if err := os.MkdirAll(b.workspaceDir(name), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// resolverFile decides what a sandbox should read for DNS, and is not as obvious as binding the
// machine's own file.
//
// When the machine resolves through a loopback address — systemd-resolved at 127.0.0.53, which is
// what most distributions do — a sandbox cannot use it: 127.0.0.1 inside a sandbox is the
// sandbox's own loopback, where nothing listens. The measured symptom is an `apt-get update` that
// works on the host and fails in the sandbox. The machine's upstream servers are in a file of
// their own, so that is what gets bound; everything else binds the machine's file unchanged.
func (b *Bwrap) resolverFile(workspaceDir string) (string, error) {
	raw, err := os.ReadFile(b.resolver.Host)
	if err != nil {
		// No resolver file to read: binding the machine's path is what the argv did before, and
		// a sandbox with no DNS says so when a command tries to resolve something.
		return "", nil
	}
	if !hasLoopbackServers(string(raw)) {
		return "", nil
	}
	upstream, err := os.ReadFile(b.resolver.Upstream)
	if err != nil {
		return "", nil
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(workspaceDir, "resolv.conf")
	if err := os.WriteFile(path, upstream, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// hasLoopbackServers reports whether a resolver file names an address that only means something
// on the machine itself: 127.0.0.0/8 and ::1.
func hasLoopbackServers(contents string) bool {
	for line := range strings.SplitSeq(contents, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		address := fields[1]
		if strings.HasPrefix(address, "127.") || address == "::1" {
			return true
		}
	}
	return false
}

// idleTimeoutOrDefault is the policy's idle timeout, or the default when the policy did not name
// one. The default normally comes from Policy.withDefaults, which the microVM driver calls while
// rendering its file; a driver that reads the field directly has to ask, or a sandbox that nobody
// told about the timer keeps running forever.
func idleTimeoutOrDefault(value string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return DefaultIdleTimeout
}
