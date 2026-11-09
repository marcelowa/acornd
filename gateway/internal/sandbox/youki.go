package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/supervise"
)

// Youki is the OCI-runtime engine: user, mount, pid, network, ipc and uts namespaces declared in
// a config.json instead of an argv, with the workspace's own image as the root. It is the engine
// for a machine without a hypervisor, and the only one of the three that can state a policy —
// capabilities, devices, rlimits, read-only and masked paths — rather than only dropping it.
//
// The driver installs the pinned runtime, answers the machine's questions, writes the record and
// the OCI configuration, and boots a container whose first process is the runtime's own version
// in step 2. The supervisor and the worker's socket client are step 3; the engine is not in
// EngineNames until they land, because a workspace whose commands cannot run is not one a
// chooser should offer. See issue #47 and docs/adr/0022-youki-engine.md.
type Youki struct {
	dataDir string
	// state is where one record and one OCI bundle per workspace live.
	state string
	// base is where the pinned release is fetched from. Set in tests to a local server, so the
	// download, the checksum and the unpack are exercised rather than assumed.
	base string
	// checksum answers the release tarball's digest for an architecture. It is a field so a
	// test can pin a fixture's digest without changing the constant the product ships.
	checksum func(goarch string) string
	client   *http.Client
	// runner runs the runtime. It is an interface so the probes can be answered without the
	// binary, the same way bwrap's runner is.
	runner Runner
	look   func(string) (string, error)
	// pull fetches an image into a directory. It is a field so a test can answer without a
	// registry.
	pull imageSource
	// resolver is the machine's resolver file and the file its upstream servers are in.
	resolver resolverFiles
	// pasta is the egress probe, a field so a test can answer without running one.
	pasta func(string) error
	// devices are the host paths a sandbox may be given, so a test can answer for a machine
	// that has neither without making the suite depend on this one.
	devices hostDevices
	// cgroup answers whether this machine lets a sandbox have a budget. It is a field so a test
	// can answer for a machine that is not this one.
	cgroup func() error
}

// youkiVersion is the release this build runs. A newer runtime is a new measurement, not a bump:
// the three rules in youki_spec.go are properties of this release's generated configuration.
const youkiVersion = "0.7.0"

// youkiReleaseBase is where the pinned release's assets live. youki is in no distribution, so
// vendoring the static tarball is the only route (ADR 0022).
const youkiReleaseBase = "https://github.com/youki-dev/youki/releases/download"

// youkiSHA256 is the digest of the release tarball for an architecture, measured from the
// release itself. One value per platform this build can run: a checksum that covers every
// architecture would verify one of them and be a guess for the rest.
func youkiSHA256(goarch string) string {
	switch goarch {
	case "amd64":
		return "a211a16b6b1a985f8ca61bba9a30b9ae40352d04dd7eca04cdecfe7b98b47038"
	case "arm64":
		return "b96c05c2c82f1d20a74b611188fa120894c50a6128f73856bb371604ecb69bd0"
	}
	return ""
}

// youkiArtifact names the release asset for a platform, or "" when the release publishes none.
// The release uses Unix names for both fields, which is why they are mapped rather than passed
// through, and the musl build is the static one a sandbox can run.
func youkiArtifact(goos, goarch string) string {
	if goos != "linux" {
		return ""
	}
	var arch string
	switch goarch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	default:
		return ""
	}
	return "youki-" + youkiVersion + "-" + arch + "-musl.tar.gz"
}

// NewYouki builds the driver. The runtime is installed on first use, as the microVM engine is.
func NewYouki(dataDir string) *Youki {
	return &Youki{
		dataDir:  dataDir,
		state:    filepath.Join(dataDir, "sandboxes", "youki"),
		base:     youkiReleaseBase + "/v" + youkiVersion,
		checksum: youkiSHA256,
		look:     exec.LookPath,
		pull:     pullImage,
		resolver: resolverFiles{Host: "/etc/resolv.conf", Upstream: "/run/systemd/resolve/resolv.conf"},
		devices:  hostDevices{tun: "/dev/net/tun", kvm: "/dev/kvm"},
		cgroup:   probeCgroup,
	}
}

// WithStateDir moves the engine's folder, as the other engines' drivers do.
func (y *Youki) WithStateDir(state string) *Youki {
	y.state = filepath.Join(state, "youki")
	return y
}

// WithRunner replaces the runtime binary, which is how the driver is tested without one.
func (y *Youki) WithRunner(runner Runner) *Youki {
	y.runner = runner
	return y
}

// WithLookPath answers "is pasta installed" for a test, which must not depend on this machine.
func (y *Youki) WithLookPath(look func(string) (string, error)) *Youki {
	y.look = look
	return y
}

// WithImageSource replaces the registry with a directory of its own, as bwrap's driver does.
func (y *Youki) WithImageSource(pull imageSource) *Youki {
	y.pull = pull
	return y
}

// WithDevicePaths answers "does this machine have them" for a test, which must not depend on
// the device nodes of the machine running it.
func (y *Youki) WithDevicePaths(tun, kvm string) *Youki {
	y.devices = hostDevices{tun: tun, kvm: kvm}
	return y
}

func (y *Youki) Name() string { return "youki" }

// Version is the pinned release, which is what an install fetches. Installed says whether it is
// here; a version read from a binary somebody replaced would be a claim about a runtime this
// build did not measure.
func (y *Youki) Version() string { return youkiVersion }

// Isolated is true: youki separates what runs from the host with namespaces. It is a weaker
// boundary than a microVM — the kernel is shared — which is why the interface names the engine
// rather than only saying that a workspace is isolated.
func (y *Youki) Isolated() bool { return true }

// InstallDir is the folder the release is unpacked into; binary is the one file it holds.
func (y *Youki) InstallDir() string { return filepath.Join(y.dataDir, "tools", "youki") }

// binary is where the vendored runtime lives, `<install>/youki/youki` below the data folder.
func (y *Youki) binary() string { return filepath.Join(y.InstallDir(), "youki") }

func (y *Youki) StateDir() string { return y.state }

// Env is where the worker finds the records this engine writes. The worker's client reads
// `<state>/<name>.json`, the same file a person can read.
func (y *Youki) Env() []string { return []string{"ACORND_YOUKI_STATE=" + y.state} }

// Installed reports whether the pinned binary is on disk and executable. A file that is there
// but cannot run is not an installed engine: the probe would fail on every command.
func (y *Youki) Installed() bool {
	info, err := os.Stat(y.binary())
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

// Install fetches the pinned release, verifies it against the digest this build carries, and
// unpacks it beside its final name, so a half-downloaded runtime is never mistaken for one. The
// pattern is the microVM engine's (ADR 0012); the checksum is the part youki needs more, because
// it is in no distribution and a package manager is not checking it.
func (y *Youki) Install(ctx context.Context, log func(string, ...any)) error {
	if y.Installed() {
		return nil
	}
	artifact, want, err := y.release(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if log != nil {
		log("installing the youki runtime %s (%s)", youkiVersion, artifact)
	}
	staging := y.InstallDir() + ".partial"
	// Scratch whether this works or not: a half-unpacked runtime beside the install is a
	// download nobody can resume.
	defer os.RemoveAll(staging)
	if err := y.stage(ctx, artifact, want, staging); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(y.InstallDir()), 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(y.InstallDir()); err != nil {
		return err
	}
	if err := os.Rename(staging, y.InstallDir()); err != nil {
		return err
	}
	if log != nil {
		log("youki runtime installed at %s", y.InstallDir())
	}
	return nil
}

// release names the asset and its pinned digest for a platform, or refuses with the platform
// the release does not build for.
func (y *Youki) release(goos, goarch string) (artifact, want string, err error) {
	artifact = youkiArtifact(goos, goarch)
	if artifact == "" {
		return "", "", fmt.Errorf("%w: youki has no release for %s/%s", ErrUnavailable, goos, goarch)
	}
	want = y.checksum(goarch)
	if want == "" {
		return "", "", fmt.Errorf("%w: this build has no checksum for youki on %s, so the release cannot be verified", ErrUnavailable, goarch)
	}
	return artifact, want, nil
}

// stage downloads one release into scratch, verifies it, and leaves the runtime where the
// install can be swapped for it.
func (y *Youki) stage(ctx context.Context, artifact, want, staging string) error {
	client := y.client
	if client == nil {
		client = http.DefaultClient
	}
	body, err := fetch(ctx, client, y.base+"/"+artifact)
	if err != nil {
		return err
	}
	defer body.Close()
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	if err := unpackVerified(body, want, staging); err != nil {
		return err
	}
	binary := filepath.Join(staging, "youki")
	info, err := os.Stat(binary)
	if err != nil || info.IsDir() {
		return fmt.Errorf("%w: %s did not contain a youki binary", ErrUnavailable, artifact)
	}
	return os.Chmod(binary, 0o755)
}

// run drives the runtime. Nothing is inherited from the gateway's environment: the probes read
// only what youki prints.
func (y *Youki) run(ctx context.Context, args ...string) (string, error) {
	if y.runner != nil {
		return y.runner.Run(ctx, args...)
	}
	if !y.Installed() {
		return "", fmt.Errorf("%w: the youki runtime is not installed at %s", ErrUnavailable, y.binary())
	}
	command := exec.CommandContext(ctx, y.binary(), args...)
	// A runtime can leave a child holding the output pipe, and CombinedOutput waits for every
	// writer to close it: the wait is bounded so a probe is a sentence rather than a hang.
	command.WaitDelay = time.Second
	output, err := command.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("youki: %s", firstLine(text))
	}
	return text, nil
}

// Usable is a real probe: a user namespace is the one thing a youki sandbox cannot do without,
// and the runtime's own `spec --rootless` is the cheapest way to ask whether this machine makes
// one. It runs in a child process on purpose — unshare(CLONE_NEWUSER) returns EINVAL from a Go
// process because the kernel requires the caller to be single-threaded, which would read like a
// kernel restriction on every machine.
func (y *Youki) Usable() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("%w: youki is a Linux runtime, and this machine is %s", ErrUnavailable, runtime.GOOS)
	}
	if !y.Installed() {
		return fmt.Errorf("%w: the youki runtime is not installed at %s (acornd fetches the pinned release on first use)", ErrUnavailable, y.binary())
	}
	dir, err := os.MkdirTemp("", "acornd-youki-probe-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := y.run(ctx, "spec", "--rootless", "--bundle", dir)
	if err != nil {
		detail := strings.TrimSpace(out)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("%w: youki cannot make a rootless configuration here, which is what a user namespace needs: %s (Ubuntu 23.10 and later restrict them with AppArmor; fix: sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0)", ErrUnavailable, lastLine(detail))
	}
	if err := y.cgroup(); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return nil
}

// Egress reports whether a workspace with its network on can run here: pasta carries it, and
// pasta does not depend on the engine. A machine without pasta can still run a workspace with
// its network off, which is why this is not Usable.
func (y *Youki) Egress() error { return probeEgress(y.look, y.pasta) }

// Nesting reports whether a hypervisor can run in a youki sandbox: the machine needs /dev/kvm,
// and the runtime needs to be the release whose nesting behaviour was measured. It is a
// capability, not a policy; whether a workspace asks for it is the policy's business.
func (y *Youki) Nesting(context.Context) NestingSupport {
	if _, err := os.Stat(y.devices.kvm); err != nil {
		return unavailable("no /dev/kvm on this machine, so a hypervisor inside a youki sandbox cannot start")
	}
	if !y.Installed() {
		return unavailable("the youki runtime is not installed at %s, so the version whose nesting behaviour is measured cannot be checked", y.binary())
	}
	version := y.runtimeVersion()
	if version == "" {
		return unavailable("the youki runtime at %s did not report its version, and a container that nests needs %s or later", y.binary(), youkiVersion)
	}
	if !versionAtLeast(version, youkiVersion) {
		return unavailable("the youki runtime is %s and a container that nests needs %s or later (that is the release whose CAP_SETFCAP behaviour was measured)", version, youkiVersion)
	}
	return NestingSupport{Available: true}
}

// runtimeVersion is the installed binary's own answer, read once per call: it is only asked by
// Nesting, which is a doctor or a state-frame question rather than a command's.
func (y *Youki) runtimeVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := y.run(ctx, "--version")
	if err != nil {
		return ""
	}
	return youkiVersionFrom(out)
}

// youkiVersionFrom reads the release out of "youki version: 0.7.0" and the lines after it.
func youkiVersionFrom(output string) string {
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "youki" && fields[1] == "version:" {
			return fields[2]
		}
	}
	return ""
}

// versionAtLeast compares two dotted versions component by component. A version this build
// cannot read is not at least anything: a runtime whose answer is a guess must not be trusted
// with a container that nests.
func versionAtLeast(got, want string) bool {
	have, ok := parseVersion(got)
	if !ok {
		return false
	}
	required, ok := parseVersion(want)
	if !ok {
		return false
	}
	for index := range have {
		if have[index] != required[index] {
			return have[index] > required[index]
		}
	}
	return true
}

// parseVersion reads the three numeric components of a dotted version, stopping each at the
// first character that is not a digit so a release candidate compares as its release.
func parseVersion(value string) ([3]int, bool) {
	var version [3]int
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) < 3 {
		return version, false
	}
	for index := range version {
		digits := 0
		for digits < len(parts[index]) && parts[index][digits] >= '0' && parts[index][digits] <= '9' {
			digits++
		}
		if digits == 0 {
			return version, false
		}
		number, err := strconv.Atoi(parts[index][:digits])
		if err != nil {
			return version, false
		}
		version[index] = number
	}
	return version, true
}

// youkiConfig is what one workspace's commands run under: the same record shape the bwrap engine
// writes, with the keys the worker's socket client reads, plus the paths this engine needs to
// build its OCI configuration. A person can read it; the runtime never does.
type youkiConfig struct {
	Engine    string         `json:"engine"`
	Workspace string         `json:"workspace"`
	Image     string         `json:"image"`
	Network   sandboxNetwork `json:"network"`
	// Nested is whether this sandbox was created inside another sandbox. The worker uses it to
	// decide which engine an inner acornd may pick; the OCI configuration does not change,
	// because a youki container that nests needs the same three rules at every depth.
	Nested bool `json:"nested,omitempty"`
	// Rootfs is the workspace's own unpacked copy of its image, and the runtime's root.path. It
	// is where an install inside the sandbox lands and stays.
	Rootfs  string `json:"rootfs,omitempty"`
	DataDir string `json:"home"`
	// Hidden is the install folder the worker's record shape names. A youki sandbox's root is
	// its image, so there is nothing of the install folder in it to hide.
	Hidden   string   `json:"hidden"`
	ReadOnly []string `json:"readOnly,omitempty"`
	// ResolvConf is the file bound at /etc/resolv.conf: the machine's own, or a copy of its
	// upstreams when the machine's points at a loopback resolver the sandbox cannot reach.
	ResolvConf string `json:"resolvConf,omitempty"`
	// Youki is the vendored runtime, and Supervisor is this binary bound in at guestSupervisor:
	// the same program bwrap runs, so the worker's client is the same.
	Youki      string `json:"youki"`
	Supervisor string `json:"supervisor"`
	// Spec is the generated OCI configuration. The bundle is the folder it sits in, and the
	// record is the file beside it.
	Spec string `json:"spec"`
	// CgroupPath is the absolute cgroup the runtime creates: /acornd.slice plus this sandbox's
	// name. An absolute path is what selects youki's cgroupfs manager; a relative one would
	// select systemd and need a session bus a sandbox lacks.
	CgroupPath  string `json:"cgroupPath,omitempty"`
	Socket      string `json:"socket"`
	GuestSocket string `json:"guestSocket"`
	Run         string `json:"run"`
	GuestRun    string `json:"guestRun"`
	// IdleTimeout stops a sandbox nothing has run in for a while. It is the same policy value
	// the other engines are given, so every engine answers the question the same way.
	IdleTimeout string `json:"idleTimeout"`
	// SecretProxy is phase 2 (ADR 0031). The record keeps the key the worker reads; this build
	// delivers secrets as environment values and writes no proxy.
	SecretProxy *sandboxSecretProxy `json:"secretProxy,omitempty"`
	// Devices is whether this sandbox received the host's /dev/net/tun and /dev/kvm, and
	// DeviceNodes is what the runtime creates for them. The numbers come from the host's own
	// nodes, so a machine without one gets no entry rather than a guess.
	Devices     bool        `json:"devices,omitempty"`
	DeviceNodes []ociDevice `json:"deviceNodes,omitempty"`
	// DisableNesting is whether this sandbox must not nest at all. It drops the /proc masks and
	// CAP_SETFCAP from the generated configuration; the record keeps the policy's own word.
	DisableNesting bool `json:"disableNesting,omitempty"`
	// CPUs and Memory are the policy's budget, recorded so the next start generates the same
	// cgroup and a person can see what the sandbox may take.
	CPUs   int    `json:"cpus,omitempty"`
	Memory string `json:"memory,omitempty"`
	// Beta notes what this shape does not do yet, so the file does not read as a promise.
	Beta string `json:"beta"`
}

// youkiBeta is what one mapped id costs, in the record a person reads. The rootfs carries the
// workaround (rootfs.go writes APT::Sandbox::User root); this is the rest of the cost.
const youkiBeta = "one user id is mapped (0 ↔ the user): chown to another id fails, and the root carries APT::Sandbox::User root for apt"

// Prepare writes the record and the OCI configuration the next start will read, without starting
// anything. Produce calls it for an existing sandbox so a changed policy is written for the next
// start; the running container keeps the shape it booted with.
func (y *Youki) Prepare(_ context.Context, policy Policy, _ string) error {
	_, err := y.writeConfig(policy)
	return err
}

// Create resolves the policy into a record and a bundle, then boots it: control's policy is
// "ensure created, then ensure running", and a sandbox that is created but not running is a
// workspace whose tools do not work.
func (y *Youki) Create(ctx context.Context, policy Policy, dir string) error {
	if err := y.Prepare(ctx, policy, dir); err != nil {
		return err
	}
	return y.Start(ctx, Name(policy.Workspace), policy.Secrets)
}

// Start boots a sandbox that exists and is stopped. A container the runtime still has running
// is adopted: a gateway that restarted finds the sandbox it started before and does not put a
// second one over the same workspace. Anything else is deleted first, because a created but
// never-started container, or one left by a start that failed, owns the id the create below
// needs.
func (y *Youki) Start(ctx context.Context, name string, _ []Secret) error {
	config, err := y.readConfig(name)
	if err != nil {
		return err
	}
	if y.adopt(ctx, name, config) {
		return nil
	}
	return y.boot(ctx, name, config)
}

// adopt reports whether the runtime's container is still the sandbox this record names: the
// runtime says running and, for a real driver, the supervisor's socket answers. The fake runner
// has no socket, so a unit test of the argv trusts the state.
func (y *Youki) adopt(ctx context.Context, name string, config youkiConfig) bool {
	if y.runtimeState(ctx, name) != "running" {
		return false
	}
	if y.runner != nil {
		return true
	}
	return supervise.WaitForReady(ctx, config.Socket, 2*time.Second) == nil
}

// boot deletes a container that is not the one this record names — created but never started,
// or left by a start that failed — then creates, bounds and starts one, and waits for the
// supervisor. Network off is the runtime's create/start; egress is pasta carrying a `youki run`
// that stays for the container's life. The budget is written before the first command either
// way: between create and start, or right after the supervisor answers.
func (y *Youki) boot(ctx context.Context, name string, config youkiConfig) error {
	// Best effort: there is no container in the common case, and a delete that finds nothing
	// says so with an error this path does not need.
	_ = y.delete(ctx, name)
	if err := y.launch(ctx, name, config); err != nil {
		return err
	}
	if y.runner != nil {
		return nil
	}
	if err := y.waitForSupervisor(ctx, name, config); err != nil {
		return err
	}
	if config.Network.Mode == "pasta" {
		// The runtime wrote memory.max and cpu.max at create; the swap limit is ours, and the
		// worker has not attached yet, so the first command is already bounded.
		if err := y.enforceBudget(config.CgroupPath); err != nil {
			_ = y.Stop(context.Background(), name)
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}
	return nil
}

// launch is the create/start pair the network mode decides: the runtime's create and start with
// an empty namespace, or pasta carrying a `youki run` that stays for the container's life.
func (y *Youki) launch(ctx context.Context, name string, config youkiConfig) error {
	if config.Network.Mode == "pasta" {
		return y.startPasta(name, config)
	}
	if err := y.create(ctx, name); err != nil {
		return err
	}
	if y.runner == nil {
		if err := y.enforceBudget(config.CgroupPath); err != nil {
			_ = y.Stop(context.Background(), name)
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}
	return y.start(ctx, name)
}

// waitForSupervisor waits for the ready frame, and stops the sandbox it just started when the
// supervisor never answers.
func (y *Youki) waitForSupervisor(ctx context.Context, name string, config youkiConfig) error {
	if err := supervise.WaitForReady(ctx, config.Socket, 15*time.Second); err != nil {
		detail := y.logTail(y.logPath(name))
		_ = y.Stop(context.Background(), name)
		if detail != "" {
			return fmt.Errorf("%w: %w: %s", ErrUnavailable, err, detail)
		}
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return nil
}

// Stop ends the sandbox and keeps every file: the runtime's delete kills the processes, and
// only then is the cgroup empty enough for the kernel to let it go. Removing the cgroup first
// leaves a live container with no budget, which the spike measured as the kernel keeping the
// cgroup alive. The rootfs, the home and the OCI configuration stay for the next start.
func (y *Youki) Stop(ctx context.Context, name string) error {
	// A runtime that is not installed cannot have a container; a delete that fails with the
	// runtime here is a container still holding processes, which the caller has to see. The
	// parent is stopped either way, so a failed delete does not leave pasta forwarding.
	deleteErr := y.delete(ctx, name)
	y.stopRuntime(name)
	cgroupPath := ""
	if config, err := y.readConfig(name); err == nil {
		cgroupPath = config.CgroupPath
	}
	y.removeCgroup(cgroupPath)
	_ = os.Remove(y.pidPath(name))
	if deleteErr != nil && y.Installed() {
		return deleteErr
	}
	return nil
}

// Remove deletes the record, the bundle — the rootfs, the private home and the OCI configuration
// — and the socket's folder. The workspace folder is the user's and is not touched.
func (y *Youki) Remove(ctx context.Context, name string) error {
	// Removing a running sandbox removes its files but leaves the runtime's process and its
	// commands (ADR 0030). Stop is idempotent, and a stop that cannot end the container stops
	// the removal here rather than deleting the files under a live one.
	if err := y.Stop(ctx, name); err != nil {
		return err
	}
	// Read before removing: the record is what names the socket's folder, and it lives outside
	// everything else this deletes.
	run := ""
	if config, err := y.readConfig(name); err == nil {
		run = config.Run
	}
	if err := os.Remove(y.configPath(name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if run != "" {
		_ = os.RemoveAll(run)
	}
	return os.RemoveAll(y.workspaceDir(name))
}

// List reports the sandboxes this engine knows, running or stopped: one per record, which is
// what makes a stopped sandbox startable rather than remade. The runtime's state only says
// whether a record's container is still running; the record is what a sandbox is.
func (y *Youki) List(ctx context.Context) ([]Instance, error) {
	entries, err := os.ReadDir(y.StateDir())
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
		config, err := y.readConfig(name)
		if err != nil {
			continue
		}
		status := "stopped"
		if y.runtimeState(ctx, name) == "running" {
			status = "running"
		}
		instances = append(instances, Instance{Name: name, Image: config.Image, Status: status})
	}
	return instances, nil
}

// create is the runtime's create: the bundle is this workspace's folder, and the pid file is
// where a person can find the container's init. The output goes to a file rather than a pipe —
// the container's init inherits it, and a CombinedOutput would wait for an EOF that never comes
// (issue #47 §10 trap 2).
func (y *Youki) create(ctx context.Context, name string) error {
	args := []string{"create", "--bundle", y.workspaceDir(name), "--pid-file", y.pidPath(name), name}
	if err := y.runLogged(ctx, y.logPath(name), args...); err != nil {
		return fmt.Errorf("%w: youki create: %w", ErrUnavailable, err)
	}
	return nil
}

// enforceBudget writes the one limit the runtime did not: youki 0.7.0 left memory.swap.max at
// "max" in the measured run, and with swap available a 400 MB allocation in a 256 M workspace
// survived by swapping. memory.max and cpu.max are the runtime's; this is the belt that makes
// memory.max bind. It runs after create and before start, so the first command is already
// bounded.
func (y *Youki) enforceBudget(cgroupPath string) error {
	path := filepath.Join(cgroupDir(cgroupPath), "memory.swap.max")
	if err := os.WriteFile(path, []byte("0"), 0o644); err != nil {
		return fmt.Errorf("the sandbox's swap limit at %s: %w", path, err)
	}
	return nil
}

// pastaArgv is the boot command for a workspace with egress: pasta makes the network namespace
// and forwards its traffic, acornd's netns helper configures it and refuses the ranges the
// workspace was not granted, and `youki run` creates the container inside it. The container's
// spec omits the network namespace, so the container's init joins pasta's.
func (y *Youki) pastaArgv(config youkiConfig, name string) ([]string, error) {
	if config.Network.Pasta == "" || config.Supervisor == "" {
		return nil, fmt.Errorf("sandbox: this workspace has egress but its record has no pasta or acornd path: recreate the sandbox")
	}
	address, prefix, ok := strings.Cut(config.Network.Address, "/")
	if !ok || address == "" || prefix == "" {
		return nil, fmt.Errorf("sandbox: the record's network address %q is not an address with a prefix", config.Network.Address)
	}
	argv := []string{
		config.Network.Pasta, "-q",
		"-t", "none", "-u", "none", "-T", noForwarding, "-U", "none",
		"--no-map-gw",
	}
	if !config.Network.Private {
		argv = append(argv, "--dns-forward", sandboxDNS)
	}
	argv = append(argv, "-a", address, "-n", prefix, "-g", config.Network.Gateway, "--")
	configure := []string{
		config.Supervisor, "internal", "netns",
		"--address", config.Network.Address,
		"--gateway", config.Network.Gateway,
	}
	if !config.Network.Private {
		configure = append(configure, refusePrivate)
	}
	configure = append(configure, y.binary(), "run", "--bundle", y.workspaceDir(name), "--pid-file", y.pidPath(name), name)
	return append(argv, configure...), nil
}

// startPasta runs the boot command in its own session: pasta and the runtime must outlive the
// gateway that started them, and `youki run` stays for the container's life, which is what
// keeps pasta forwarding. Its output goes to the same log the runtime writes.
func (y *Youki) startPasta(name string, config youkiConfig) error {
	argv, err := y.pastaArgv(config, name)
	if err != nil {
		return err
	}
	log, err := os.OpenFile(y.logPath(name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	command := exec.Command(argv[0], argv[1:]...)
	command.Stdout = log
	command.Stderr = log
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.WaitDelay = time.Second
	if err := command.Start(); err != nil {
		return fmt.Errorf("%w: starting pasta: %w", ErrUnavailable, err)
	}
	if err := os.WriteFile(y.runtimePidPath(name), []byte(strconv.Itoa(command.Process.Pid)), 0o600); err != nil {
		_ = command.Process.Kill()
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}

// stopRuntime ends the pasta/runtime process a start left behind. `youki delete --force` has
// already killed the container; this is the parent that keeps pasta's namespace alive, and it
// must not outlive the sandbox.
func (y *Youki) stopRuntime(name string) {
	raw, err := os.ReadFile(y.runtimePidPath(name))
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		_ = os.Remove(y.runtimePidPath(name))
		return
	}
	process, err := os.FindProcess(pid)
	if err == nil {
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
	_ = os.Remove(y.runtimePidPath(name))
}

// runtimePidPath is the pasta/runtime process, which is not the container's init: the init pid
// is the runtime's own file, and this one is what a stop signals.
func (y *Youki) runtimePidPath(name string) string {
	return filepath.Join(y.workspaceDir(name), "runtime.pid")
}

// start is the runtime's start: the init was created by create, and this tells it to go.
func (y *Youki) start(ctx context.Context, name string) error {
	if err := y.runLogged(ctx, y.logPath(name), "start", name); err != nil {
		return fmt.Errorf("%w: youki start: %w", ErrUnavailable, err)
	}
	return nil
}

// delete is the runtime's delete with --force, so a stopped sandbox does not depend on being
// down first. A container the runtime does not have is an error the caller decides about.
func (y *Youki) delete(ctx context.Context, name string) error {
	_, err := y.run(ctx, "delete", "--force", name)
	return err
}

// runLogged runs the runtime with its output going to a file. The file is the point: a pipe
// would keep CombinedOutput waiting for an EOF that only the container's exit closes. WaitDelay
// is the belt for a runtime that leaves a child behind.
func (y *Youki) runLogged(ctx context.Context, logPath string, args ...string) error {
	if y.runner != nil {
		_, err := y.runner.Run(ctx, args...)
		return err
	}
	if !y.Installed() {
		return fmt.Errorf("the youki runtime is not installed at %s", y.binary())
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	command := exec.CommandContext(ctx, y.binary(), args...)
	command.Stdout = log
	command.Stderr = log
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		if detail := y.logTail(logPath); detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}

// removeCgroup removes the leaf the runtime created, at the absolute path the record names. It
// is best effort: a cgroup that is not there is the wanted state, and Remove, not RemoveAll,
// keeps a cgroup with a live process in it.
func (y *Youki) removeCgroup(cgroupPath string) {
	if cgroupPath == "" {
		return
	}
	_ = os.Remove(cgroupDir(cgroupPath))
}

// logPath is the runtime's own output for one workspace, beside the bundle.
func (y *Youki) logPath(name string) string { return filepath.Join(y.workspaceDir(name), "youki.log") }

// pidPath is where the runtime writes the container's init pid. It is for a person and for a
// delete that has to find a process; the record and `youki state` are what the lifecycle reads.
func (y *Youki) pidPath(name string) string {
	return filepath.Join(y.workspaceDir(name), "container.pid")
}

// logTail is the last few lines of a runtime log, for an error that would otherwise be a bare
// exit status.
func (y *Youki) logTail(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, "; ")
}

// runtimeState is what the runtime says about one container, or "" when it has none. It is read
// for the status only: rootless youki's `list` was unreliable in the spike, so the record stays
// the source of what exists (ADR 0022 §4).
func (y *Youki) runtimeState(ctx context.Context, name string) string {
	out, err := y.run(ctx, "state", name)
	if err != nil {
		return ""
	}
	var state struct {
		Status string `json:"status"`
	}
	if json.Unmarshal([]byte(out), &state) != nil {
		return ""
	}
	return state.Status
}

// SecretDelivery is the environment: phase 1 of ADR 0021's container row. A value reaches the
// command's environment and nothing else; the substitution proxy is ADR 0031 and is not this
// release.
func (y *Youki) SecretDelivery() Delivery { return DeliveryEnvironment }

// SecretEnvInWorker is true: the value has to be in the worker's process for the supervisor to
// start a command with it. The sandbox's own record never carries it.
func (y *Youki) SecretEnvInWorker() bool { return true }

// SecretsChanged cannot change a running process's environment. The worker is restarted, which
// reads the new set on the way up.
func (y *Youki) SecretsChanged(context.Context, string, []Secret) (SecretChange, error) {
	return SecretChange{RestartRequired: true}, nil
}

// writeConfig is the record and the OCI configuration, both written for a person to read and for
// the next start to use. The record is written last: it is the marker that a sandbox exists.
func (y *Youki) writeConfig(policy Policy) (youkiConfig, error) {
	config, name, err := y.record(policy)
	if err != nil {
		return youkiConfig{}, err
	}
	spec, err := youkiSpec(config, name)
	if err != nil {
		return youkiConfig{}, err
	}
	specData, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return youkiConfig{}, err
	}
	if err := os.WriteFile(config.Spec, append(specData, '\n'), 0o644); err != nil {
		return youkiConfig{}, err
	}
	recordData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return youkiConfig{}, err
	}
	if err := os.WriteFile(y.configPath(name), append(recordData, '\n'), 0o600); err != nil {
		return youkiConfig{}, err
	}
	return config, nil
}

// record resolves the policy into everything the configuration needs, without writing either
// file: the paths, the network, the resolver, the socket and the supervisor.
func (y *Youki) record(policy Policy) (youkiConfig, string, error) {
	policy = policy.withDefaults()
	if err := validateWorkspace(policy); err != nil {
		return youkiConfig{}, "", err
	}
	name := Name(policy.Workspace)
	if err := y.stateDirs(name); err != nil {
		return youkiConfig{}, "", err
	}
	rootfs, err := workspaceRootfs(policy, y.StateDir(), name, policy.Image, y.pull)
	if err != nil {
		return youkiConfig{}, "", err
	}
	network, err := y.networkFor(policy)
	if err != nil {
		return youkiConfig{}, "", err
	}
	workspaceDir := y.workspaceDir(name)
	resolvConf, err := resolveResolvFor(workspaceDir, y.resolver, network.Mode, network.Private)
	if err != nil {
		return youkiConfig{}, "", err
	}
	run, socket, err := sandboxSocketPaths(y.dataDir, policy.Workspace)
	if err != nil {
		return youkiConfig{}, "", err
	}
	supervisor, err := os.Executable()
	if err != nil {
		return youkiConfig{}, "", fmt.Errorf("sandbox: cannot find this binary to run the sandbox's supervisor: %w", err)
	}
	return youkiConfig{
		Engine:         y.Name(),
		Workspace:      policy.Workspace,
		Image:          policy.Image,
		Network:        network,
		Nested:         nestedFromProcess(),
		Rootfs:         rootfs,
		DataDir:        filepath.Join(workspaceDir, "home"),
		Hidden:         y.dataDir,
		ReadOnly:       readonlyGlobals(policy),
		ResolvConf:     resolvConf,
		Youki:          y.binary(),
		Supervisor:     supervisor,
		Spec:           filepath.Join(workspaceDir, "config.json"),
		CgroupPath:     cgroupPathFor(name),
		Socket:         socket,
		GuestSocket:    guestSocketPath,
		Run:            run,
		GuestRun:       guestRunDir,
		IdleTimeout:    idleTimeoutOrDefault(policy.IdleTimeout),
		Devices:        policy.Devices,
		DeviceNodes:    y.deviceNodes(policy.Devices),
		DisableNesting: policy.DisableVMNesting,
		CPUs:           policy.CPUs,
		Memory:         policy.Memory,
		Beta:           youkiBeta,
	}, name, nil
}

// stateDirs makes the folders every youki sandbox needs: the store, this workspace's bundle,
// and the private home inside it.
func (y *Youki) stateDirs(name string) error {
	if err := os.MkdirAll(y.state, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(y.workspaceDir(name), 0o700); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(y.workspaceDir(name), "home"), 0o700)
}

// networkFor is the namespace's networking: off, or pasta carrying it. Egress without pasta is
// an install that needs one `apt install passt` and would otherwise be a sandbox that cannot
// reach anything while claiming it can.
func (y *Youki) networkFor(policy Policy) (sandboxNetwork, error) {
	if !policy.Egress {
		return sandboxNetwork{Mode: NetworkOffName}, nil
	}
	pasta, err := y.look("pasta")
	if err != nil {
		return sandboxNetwork{}, fmt.Errorf("%w: egress in a youki workspace is carried by pasta (install it: apt install passt)", ErrUnavailable)
	}
	return sandboxNetwork{
		Mode: "pasta", Address: sandboxAddress, Gateway: sandboxGateway, Pasta: pasta,
		Private: policy.PrivateEgress,
	}, nil
}

// deviceNodes is the OCI's description of the host devices a workspace was granted. A path that
// is missing or is not a character device is skipped, not refused: a workspace still opens on a
// machine without a hypervisor, and the engine says why it cannot nest.
func (y *Youki) deviceNodes(granted bool) []ociDevice {
	if !granted {
		return nil
	}
	var nodes []ociDevice
	for _, path := range []string{y.devices.tun, y.devices.kvm} {
		if path == "" {
			continue
		}
		if node, ok := hostDeviceEntry(path); ok {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

// configPath is where one workspace's record lives, named as the engine names sandboxes so a
// reader can match one to the other.
func (y *Youki) configPath(name string) string {
	return filepath.Join(y.state, name+".json")
}

// workspaceDir is this workspace's bundle: the rootfs, the private home, and config.json.
func (y *Youki) workspaceDir(name string) string {
	return filepath.Join(y.StateDir(), name)
}

// readConfig is the record control wrote, which is also what a person can read.
func (y *Youki) readConfig(name string) (youkiConfig, error) {
	raw, err := os.ReadFile(y.configPath(name))
	if err != nil {
		return youkiConfig{}, err
	}
	var config youkiConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return youkiConfig{}, fmt.Errorf("sandbox: the youki record for %s is not readable: %w", name, err)
	}
	return config, nil
}
