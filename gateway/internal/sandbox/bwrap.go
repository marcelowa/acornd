package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

// List reports nothing: there is no sandbox object to list. A workspace's config file exists,
// but nothing is running that could be stopped.
func (b *Bwrap) List(context.Context) ([]Instance, error) { return nil, nil }

// config is what one workspace's commands run under, written for a person to read and for the
// worker to build an argv from.
type bwrapConfig struct {
	Engine    string `json:"engine"`
	Workspace string `json:"workspace"`
	Image     string `json:"image"`
	Network   string `json:"network"`
	// Rootfs is the workspace's own unpacked copy of its image, bound as `/`. It is where an
	// install inside the sandbox lands and stays: the host never sees it, and removing the
	// sandbox removes it.
	Rootfs   string   `json:"rootfs,omitempty"`
	Home     string   `json:"home"`
	Hidden   string   `json:"hidden"`
	ReadOnly []string `json:"readOnly,omitempty"`
	// Beta notes what this shape does not do yet, so the file does not read as a promise.
	Beta string `json:"beta"`
}

// Create resolves the policy into a config and the workspace's private home. It is
// idempotent: creating twice rewrites the same file, which is what makes a changed policy
// apply to the next command rather than to an engine's own stored copy.
func (b *Bwrap) Create(_ context.Context, policy Policy, dir string) error {
	if strings.TrimSpace(policy.Workspace) == "" {
		return errors.New("sandbox: policy has no workspace")
	}
	info, err := os.Stat(policy.Workspace)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("sandbox: no workspace folder at %s", policy.Workspace)
	}
	image := policy.Image
	if strings.TrimSpace(image) == "" {
		image = DefaultImage
	}
	if err := os.MkdirAll(b.state, 0o700); err != nil {
		return err
	}
	home := b.workspaceHome(Name(policy.Workspace))
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	rootfs, err := b.workspaceRootfs(Name(policy.Workspace), image)
	if err != nil {
		return err
	}
	var readonly []string
	for _, path := range policy.Global {
		if _, err := os.Stat(path); err == nil {
			readonly = append(readonly, path)
		}
	}
	sort.Strings(readonly)
	network := NetworkOffName
	if policy.Egress {
		network = "on"
	}
	config := bwrapConfig{
		Engine:    b.Name(),
		Workspace: policy.Workspace,
		Image:     image,
		Network:   network,
		Rootfs:    rootfs,
		Home:      home,
		Hidden:    b.home,
		ReadOnly:  readonly,
		Beta:      "no persistent sandbox: every command is a sandbox, so no daemon outlives one",
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	// A directory the workspace may be created under, kept for the person who reads it.
	_ = dir
	return os.WriteFile(b.configPath(Name(policy.Workspace)), append(data, '\n'), 0o600)
}

// Start does nothing: commands do not run in a sandbox that has to be booted, so there is
// nothing to boot. Secrets are read from the worker's environment for every command.
func (b *Bwrap) Start(context.Context, string, []Secret) error { return nil }

// Stop does nothing: nothing is running to stop, and a command in flight is the worker's to
// kill. It is not an error, because stopping a sandbox that has no lifecycle is already the
// wanted state.
func (b *Bwrap) Stop(context.Context, string) error { return nil }

// Remove deletes the config and the workspace's private home — the things a bwrap sandbox
// accumulates. The workspace folder is the user's and is not touched.
func (b *Bwrap) Remove(_ context.Context, name string) error {
	if err := os.Remove(b.configPath(name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	// The whole folder: the root, the private home, and whatever a command left in either.
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
