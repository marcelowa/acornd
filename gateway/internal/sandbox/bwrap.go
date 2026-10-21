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
	home string
	// state is where one JSON file per workspace's config lives.
	state string
	// runner runs bubblewrap. It is an interface so the argv can be asserted in tests, and
	// the probe can be answered without a user namespace.
	runner Runner
	look   func(string) (string, error)
	// pull fetches an image into a directory. It is a field so a test can answer without a
	// registry, the same way the runner answers without a binary.
	pull imageSource

	once    sync.Once
	version string
}

// NewBwrap builds the driver. Unlike the microVM engine it installs nothing: bubblewrap is a
// distribution package, and installing one is the operator's business, not a gateway's.
func NewBwrap(home string) *Bwrap {
	return &Bwrap{
		home:  home,
		state: filepath.Join(home, "sandboxes", "bwrap"),
		look:  exec.LookPath,
		pull:  pullImage,
	}
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
		return fmt.Errorf("%w: bubblewrap cannot create a user namespace here: %s (Ubuntu 24.04+ restricts unprivileged user namespaces with AppArmor)", ErrUnavailable, lastLine(detail))
	}
	return nil
}

// Nesting reports that a hypervisor cannot run in this sandbox: the config does not bind
// /dev/kvm, and binding it would be a deliberate grant rather than a default.
func (b *Bwrap) Nesting(context.Context) NestingSupport {
	return unavailable("a bwrap sandbox does not bind /dev/kvm, so a hypervisor inside it cannot start")
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
	Home     string   `json:"home"`
	Hidden   string   `json:"hidden"`
	ReadOnly []string `json:"readOnly,omitempty"`
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
	// Beta notes what this shape does not do yet, so the file does not read as a promise.
	Beta string `json:"beta"`
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

// bwrapNetwork is the network a command gets. The tool paths are resolved when the workspace is
// created, so a missing one is a sentence then rather than a mystery in the middle of a turn.
type bwrapNetwork struct {
	Mode string `json:"mode"`
	// Address is the sandbox's own address inside that namespace, and Gateway is pasta's address
	// there. Both live inside the sandbox: nothing about them appears on the host.
	Address string `json:"address,omitempty"`
	Gateway string `json:"gateway,omitempty"`
	Pasta   string `json:"pasta,omitempty"`
	IP      string `json:"ip,omitempty"`
}

// The sandbox's network, when it has one. It is the address space QEMU's user-mode network has
// used for twenty years, and it is chosen not to collide with the ranges a machine is likely to
// have.
const (
	sandboxAddress = "10.0.2.15/24"
	sandboxGateway = "10.0.2.2"
)

// Create resolves the policy into a config and the workspace's images, then boots it: control's
// policy is "ensure created, then ensure running", and a sandbox that is created but not running
// is a workspace whose tools do not work.
//
// It is idempotent: creating twice rewrites the same config, which is what makes a changed policy
// apply at the next sandbox rather than to a copy nobody updates.
func (b *Bwrap) Create(ctx context.Context, policy Policy, dir string) error {
	if _, err := b.writeConfig(policy, dir); err != nil {
		return err
	}
	return b.Start(ctx, Name(policy.Workspace), policy.Secrets)
}

// writeConfig is Create without the boot: what the workspace runs under, written where a person
// can read it and the sandbox can be started from it later.
func (b *Bwrap) writeConfig(policy Policy, dir string) (bwrapConfig, error) {
	if strings.TrimSpace(policy.Workspace) == "" {
		return bwrapConfig{}, errors.New("sandbox: policy has no workspace")
	}
	info, err := os.Stat(policy.Workspace)
	if err != nil || !info.IsDir() {
		return bwrapConfig{}, fmt.Errorf("sandbox: no workspace folder at %s", policy.Workspace)
	}
	image := policy.Image
	if strings.TrimSpace(image) == "" {
		image = DefaultImage
	}
	if err := os.MkdirAll(b.state, 0o700); err != nil {
		return bwrapConfig{}, err
	}
	home := b.workspaceHome(Name(policy.Workspace))
	if err := os.MkdirAll(home, 0o700); err != nil {
		return bwrapConfig{}, err
	}
	rootfs, err := b.workspaceRootfs(Name(policy.Workspace), image)
	if err != nil {
		return bwrapConfig{}, err
	}
	var readonly []string
	for _, path := range policy.Global {
		if _, err := os.Stat(path); err == nil {
			readonly = append(readonly, path)
		}
	}
	sort.Strings(readonly)
	network := bwrapNetwork{Mode: NetworkOffName}
	if policy.Egress {
		pasta, err := b.look("pasta")
		if err != nil {
			return bwrapConfig{}, fmt.Errorf("%w: egress in a bwrap workspace is carried by pasta (install it: apt install passt)", ErrUnavailable)
		}
		ip, err := b.look("ip")
		if err != nil {
			return bwrapConfig{}, fmt.Errorf("%w: configuring a bwrap workspace's network needs ip (install it: apt install iproute2)", ErrUnavailable)
		}
		network = bwrapNetwork{Mode: "pasta", Address: sandboxAddress, Gateway: sandboxGateway, Pasta: pasta, IP: ip}
	}
	executable, err := os.Executable()
	if err != nil {
		return bwrapConfig{}, fmt.Errorf("sandbox: cannot find this binary to run the sandbox's supervisor: %w", err)
	}
	bwrapPath, err := b.look("bwrap")
	if err != nil {
		return bwrapConfig{}, fmt.Errorf("%w: bubblewrap is not installed (install it: apt install bubblewrap)", ErrUnavailable)
	}
	// A short folder for the socket: a Unix socket path has a hard length, and the sandbox's own
	// name (a readable slug plus a hash) is longer than that leaves room for.
	run := filepath.Join(b.StateDir(), "run", shortHash(policy.Workspace))
	if err := os.MkdirAll(run, 0o700); err != nil {
		return bwrapConfig{}, err
	}
	socket := filepath.Join(run, "s.sock")
	for _, path := range []string{socket, guestSocketPath} {
		if len(path) > socketLimit {
			return bwrapConfig{}, fmt.Errorf("sandbox: the supervisor's socket path is too long for a Unix socket: %s is %d characters and %d is the limit (move the installation folder with --state, or shorten the workspace name)", path, len(path), socketLimit)
		}
	}
	config := bwrapConfig{
		Engine:      b.Name(),
		Workspace:   policy.Workspace,
		Image:       image,
		Network:     network,
		Rootfs:      rootfs,
		Home:        home,
		Hidden:      b.home,
		ReadOnly:    readonly,
		Supervisor:  executable,
		Socket:      socket,
		GuestSocket: guestSocketPath,
		Bwrap:       bwrapPath,
		Run:         run,
		GuestRun:    guestRunDir,
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
			return fmt.Errorf("%w: %v: %s", ErrUnavailable, err, detail)
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
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
	if pid := b.pid(name); pid > 0 {
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Signal(syscall.SIGTERM)
			for waited := 0; waited < 100; waited++ {
				if !alive(pid) {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if alive(pid) {
				_ = process.Kill()
			}
		}
	}
	_ = os.Remove(b.pidPath(name))
	_ = os.Remove(config.Socket)
	return nil
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
// accumulates. The workspace folder is the user's and is not touched.
func (b *Bwrap) Remove(_ context.Context, name string) error {
	if err := os.Remove(b.configPath(name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	// The whole folder: the root, the private home, and whatever a command left in either. The
	// socket's own folder is shared with the other sandboxes and is removed separately, by the
	// path the config names.
	if config, err := b.readConfig(name); err == nil && config.Run != "" {
		_ = os.RemoveAll(config.Run)
	}
	return os.RemoveAll(b.workspaceDir(name))
}

// workspaceRootfs returns the workspace's own copy of its image, making it on first use. A
// copy rather than a shared read-only tree with a layer on top: an install inside the sandbox
// is then an ordinary write to an ordinary directory, which needs no privileged mount, no
// overlay, and no cleanup beyond removing the folder.
func (b *Bwrap) workspaceRootfs(name, image string) (string, error) {
	rootfs := filepath.Join(b.workspaceDir(name), "rootfs")
	if info, err := os.Stat(filepath.Join(rootfs, ".acornfall-rootfs")); err == nil && !info.IsDir() {
		return rootfs, nil
	}
	base, err := b.imageBase(image)
	if err != nil {
		return "", err
	}
	// Replaced whole, so a half-copied tree cannot be mistaken for a finished one.
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
	complete := filepath.Join(dir, "complete")
	if info, err := os.Stat(complete); err == nil && !info.IsDir() {
		return filepath.Join(dir, "base"), nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	staging := filepath.Join(dir, "base.partial")
	_ = os.RemoveAll(staging)
	if b.pull == nil {
		return "", errors.New("sandbox: no image source is configured")
	}
	if err := b.pull(context.Background(), image, staging, nil); err != nil {
		_ = os.RemoveAll(staging)
		return "", fmt.Errorf("sandbox: fetching the image %s: %w", image, err)
	}
	base := filepath.Join(dir, "base")
	_ = os.RemoveAll(base)
	if err := os.Rename(staging, base); err != nil {
		return "", err
	}
	if err := os.WriteFile(complete, []byte(image+"\n"), 0o644); err != nil {
		return "", err
	}
	return base, nil
}

// workspaceDir is a path inside this workspace's sandbox folder.
func (b *Bwrap) workspaceDir(name string) string {
	return filepath.Join(b.StateDir(), name)
}

// SecretDelivery is the environment: there is no guest to substitute into. A value in the
// worker's environment reaches the command, and the interface says so.
func (b *Bwrap) SecretDelivery() Delivery { return DeliveryEnvironment }

// SecretEnvInWorker puts the values in the worker's process environment, which is what a
// command inside the sandbox inherits.
func (b *Bwrap) SecretEnvInWorker() bool { return true }

// SecretsChanged cannot change what a future command's environment holds; the worker is
// restarted and reads the new set on the way up.
func (b *Bwrap) SecretsChanged(context.Context, string, []Secret) (SecretChange, error) {
	return SecretChange{RestartRequired: true}, nil
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
	if !ok || config.Network.Pasta == "" || config.Network.IP == "" {
		return nil, errors.New("sandbox: this workspace has egress but its config has no network to carry it: recreate the sandbox")
	}
	// pasta makes the namespace and the tap and configures neither, so this runs first, inside
	// pasta's own namespace, where the address and the route are ours to set.
	configure := strings.Join([]string{
		`IP="$1"; ADDR="$2"; GATEWAY="$3"; shift 3`,
		`"$IP" link set lo up`,
		`"$IP" link set eth0 up`,
		`"$IP" addr add "$ADDR" dev eth0`,
		`"$IP" route add default via "$GATEWAY"`,
		`exec "$@"`,
	}, "\n")
	argv := []string{
		config.Network.Pasta, "-q",
		"-a", address, "-n", prefix, "-g", config.Network.Gateway,
		"--", "/bin/sh", "-c", configure, "sh", config.Network.IP, config.Network.Address, config.Network.Gateway,
	}
	return append(argv, sandbox...), nil
}

// sandboxArgv is the bubblewrap command: what a command in this workspace can see, once, for as
// long as the sandbox lives.
func (b *Bwrap) sandboxArgv(config bwrapConfig) ([]string, error) {
	if config.Bwrap == "" {
		return nil, errors.New("sandbox: the config has no bubblewrap path: recreate the sandbox")
	}
	argv := []string{config.Bwrap}
	if config.Rootfs != "" {
		argv = append(argv, "--bind", config.Rootfs, "/",
			// Root inside a single mapping, so files written by root land owned by the user.
			"--uid", "0", "--gid", "0",
			// A root of its own still needs the machine's resolver.
			"--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf")
	} else {
		argv = append(argv, "--ro-bind", "/", "/", "--tmpfs", config.Hidden)
	}
	argv = append(argv, "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp")
	for _, path := range config.ReadOnly {
		argv = append(argv, "--ro-bind", path, path)
	}
	argv = append(argv,
		"--bind", config.Home, guestHome(config),
		"--bind", config.Workspace, config.Workspace,
		"--bind", config.Run, config.GuestRun,
		"--ro-bind", config.Supervisor, guestSupervisor,
		"--unshare-all")
	if config.Network.Mode != NetworkOffName {
		// Share the network namespace of whoever started bubblewrap: pasta, when there is one.
		argv = append(argv, "--share-net")
	}
	return append(argv, "--", guestSupervisor, "sandbox-supervisor", "--socket", config.GuestSocket), nil
}

// guestHome is where the private home appears inside the sandbox: the root's own home when there
// is an image, and its real path when the machine's root is the root.
func guestHome(config bwrapConfig) string {
	if config.Rootfs == "" {
		return config.Home
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
