package sandbox

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Microsandbox is the microVM engine: libkrun microVMs with a command line for the
// lifecycle and an SDK for execution. It is one implementation of Driver; the package
// comment explains what that buys.
type Microsandbox struct {
	dataDir string
	// state is where the sandboxes live, when it is not the default under the home. Sockets
	// need a short path and images take a while to fetch, so both are reasons to point it
	// somewhere deliberate.
	state     string
	runner    Runner
	client    *http.Client
	installed bool
	// base is where releases are fetched from. Set in tests to a local server, so that
	// downloading, verifying, and unpacking are exercised rather than assumed.
	base string
	// hostNested is the machine half of Nesting: what this computer can grant. It is a field
	// so that tests can answer for a machine that is not this one.
	hostNested func() NestingSupport
	// nestingMu guards the answer below, which is taken once: a running gateway does not
	// swap engine builds under itself.
	nestingMu sync.Mutex
	engine    *NestingSupport
}

// Runner runs the engine and returns its combined output. It is an interface so that the
// lifecycle can be recorded in tests: the ordinary test suite runs on machines with no
// hypervisor, and a test that needs one has to say so.
type Runner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// Version is the engine release acornd pins. The SDK the worker uses ships the same
// version, and the two share their on-disk state, so this is not a free upgrade.
//
// 0.7.6 is pinned on purpose, not left behind. 0.7.7 added a CPUID mask to its krun crate
// with the flag off and no way to ask for it, so its guests stopped seeing hardware
// virtualization — upstream issue #1778 — and the opt-in that restores it (microsandbox
// #823, `msb create --nested-virt`) is in no release yet. 0.7.6 predates the mask, so a
// guest nests whenever the machine can. Move this forward when the release carrying #823
// exists: nesting then becomes a choice per workspace instead of an engine default.
// EngineVersion is the release this build runs. The store the engine keeps carries its own
// schema and migrates itself forward, so anything that shares that store — an image cache, a
// suite's state directory — is keyed by this: a directory shared across versions is one an
// older build refuses to read, and one a newer build silently migrates.
func EngineVersion() string { return Version }

const Version = "0.7.6"

const releaseBase = "https://github.com/microsandbox/microsandbox/releases/download"

// statePathLimit is how long the engine's state directory may be. The engine puts its
// runtime sockets under it, and a Unix socket path has a hard limit of 108 bytes that the
// engine's own layout eats most of: measured, 46 characters works and 56 does not, whatever
// the sandbox is called. Checking here turns an internal engine error into a sentence about
// the installation folder.
const statePathLimit = 50

// NewMicrosandbox builds the driver. The engine is installed on first use.
func NewMicrosandbox(dataDir string) *Microsandbox {
	return &Microsandbox{dataDir: dataDir, base: releaseBase + "/v" + Version, hostNested: hostNestedSupport}
}

// WithRunner replaces the engine binary, which is how the driver is tested without one.
func (m *Microsandbox) WithRunner(runner Runner) *Microsandbox {
	m.runner = runner
	return m
}

func (m *Microsandbox) Name() string    { return "microsandbox" }
func (m *Microsandbox) Version() string { return Version }

// Isolated is the engine's reason to exist: a microVM separates the command from the host.
func (m *Microsandbox) Isolated() bool { return true }

func (m *Microsandbox) InstallDir() string {
	return filepath.Join(m.dataDir, "tools", "microsandbox", Version)
}

// StateDir is the engine's own store: images, sandbox disks, snapshots. It is inside the
// installation folder so removing acornd removes all of it, and outside the engine
// directory, which is replaced wholesale on upgrade.
func (m *Microsandbox) StateDir() string {
	if m.state != "" {
		return m.state
	}
	return filepath.Join(m.dataDir, "sandboxes")
}

// WithStateDir moves the engine's store — images, sandbox disks, snapshots — somewhere else.
// Both reasons are real: a Unix socket path has to stay short, and a machine that runs many
// installs (tests, several accounts) should not fetch every image again.
func (m *Microsandbox) WithStateDir(dir string) *Microsandbox {
	m.state = dir
	return m
}

// Env points the SDK at the same store the command line writes to.
func (m *Microsandbox) Env() []string { return []string{"MSB_HOME=" + m.StateDir()} }

func (m *Microsandbox) binary() string { return filepath.Join(m.InstallDir(), "bin", "msb") }

func (m *Microsandbox) Installed() bool {
	if m.installed {
		return true
	}
	info, err := os.Stat(m.binary())
	return err == nil && !info.IsDir()
}

// Install downloads the release, verifies it against the release's own checksums, and
// unpacks it beside its final name, so that a half-downloaded engine is never mistaken for
// one.
func (m *Microsandbox) Install(ctx context.Context, log func(string, ...any)) error {
	if m.Installed() {
		return nil
	}
	artifact := ArtifactFor(runtime.GOOS, runtime.GOARCH)
	if artifact == "" {
		return fmt.Errorf("%w: no engine release for %s/%s", ErrUnavailable, runtime.GOOS, runtime.GOARCH)
	}
	if log != nil {
		log("installing the sandbox engine %s (%s)", Version, artifact)
	}
	staging := m.InstallDir() + ".partial"
	// Scratch whether this works or not: a half-unpacked engine beside the install is a
	// download nobody can resume.
	defer os.RemoveAll(staging)
	if err := m.stage(ctx, artifact, staging); err != nil {
		return err
	}
	if err := m.replaceWith(staging); err != nil {
		return err
	}
	if log != nil {
		log("sandbox engine installed at %s", m.InstallDir())
	}
	return nil
}

// stage downloads one release into scratch, verifies it, and lays it out. Everything that can
// refuse the bundle happens here, before the install is touched.
func (m *Microsandbox) stage(ctx context.Context, artifact, staging string) error {
	client := m.client
	if client == nil {
		client = http.DefaultClient
	}
	want, err := fetchChecksum(ctx, client, m.base+"/checksums.sha256", artifact)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	body, err := fetch(ctx, client, m.base+"/"+artifact)
	if err != nil {
		return err
	}
	defer body.Close()
	if err := unpackVerified(body, want, staging); err != nil {
		return err
	}
	if err := layOut(staging); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(staging, "bin", "msb")); err != nil {
		return fmt.Errorf("%w: %s did not contain bin/msb", ErrUnavailable, artifact)
	}
	return nil
}

// replaceWith swaps the install for the staged copy. The old one goes first, so a half-moved
// install is never the thing that runs.
func (m *Microsandbox) replaceWith(staging string) error {
	if err := os.MkdirAll(filepath.Dir(m.InstallDir()), 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(m.InstallDir()); err != nil {
		return err
	}
	return os.Rename(staging, m.InstallDir())
}

// Usable checks that a sandbox can boot here. The engine would report this itself, but only
// after creating a sandbox and failing, which is too late to be a good error message and
// too late to refuse cleanly.
func (m *Microsandbox) Usable() error {
	switch runtime.GOOS {
	case "linux":
		file, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
		if err == nil {
			// Opening it answered the question; the close only gives the descriptor back.
			_ = file.Close()
			return nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: /dev/kvm is missing: the kernel needs KVM and the container or VM must pass it through", ErrUnavailable)
		}
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%w: /dev/kvm is not readable by %s%s", ErrUnavailable, os.Getenv("USER"), kvmHint())
		}
		return fmt.Errorf("%w: /dev/kvm: %w", ErrUnavailable, err)
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "kern.hv_support").Output()
		if err != nil {
			return fmt.Errorf("%w: cannot ask macOS whether it supports a hypervisor: %w", ErrUnavailable, err)
		}
		if strings.TrimSpace(string(out)) != "1" {
			return fmt.Errorf("%w: macOS reports no hypervisor support (kern.hv_support)", ErrUnavailable)
		}
		return nil
	default:
		return fmt.Errorf("%w: no hypervisor for %s", ErrUnavailable, runtime.GOOS)
	}
}

// owner is who owns files the sandbox creates: the host user, presented to the guest as
// itself so that the user's editor sees their own files. Linux has no other way to say it,
// and macOS shares files as the user already.
type owner struct {
	uid, gid int
	mapFiles bool
}

func currentOwner() owner {
	if runtime.GOOS != "linux" {
		return owner{}
	}
	return owner{uid: os.Getuid(), gid: os.Getgid(), mapFiles: true}
}

// Render is the sparse sandbox configuration the engine reads: sandbox.yaml, the whole
// policy for one sandbox. The engine says which fields it accepts; this writes only the
// ones we mean.
func (p Policy) Render() []byte { return p.render(currentOwner()) }

func (p Policy) render(o owner) []byte {
	p = p.withDefaults()
	var b strings.Builder
	fmt.Fprintf(&b, "image: %s\n", yamlString(p.Image))
	fmt.Fprintf(&b, "cpus: %d\n", p.CPUs)
	fmt.Fprintf(&b, "memory: %s\n", yamlString(p.Memory))
	fmt.Fprintf(&b, "idle_timeout: %s\n", yamlString(p.IdleTimeout))
	if !p.Egress {
		// The engine's default is an open network, so saying nothing would say yes.
		b.WriteString("network: none\n")
	}
	b.WriteString("mounts:\n")
	b.WriteString("  - " + yamlString(p.mount(o)) + "\n")
	for _, path := range p.Global {
		// `ro` is the decision, not a detail: the agent reads its instructions and runs their
		// scripts, and editing them from inside would be self-modification.
		b.WriteString("  - " + yamlString(path+":"+path+":ro") + "\n")
	}
	secrets := substitutedSecrets(p.Secrets)
	if len(secrets) > 0 {
		b.WriteString("secrets:\n")
		for index, secret := range secrets {
			// The value is never here: only the name of a host environment variable the
			// engine reads at start. A file the agent could read therefore holds no secret.
			fmt.Fprintf(&b, "  %s:\n", yamlString(secret.Name))
			fmt.Fprintf(&b, "    value: { $msb_env: %s }\n", secretEnvVar(index))
			b.WriteString("    allow: [")
			for i, host := range secret.Hosts {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(yamlString(host))
			}
			b.WriteString("]\n")
			fmt.Fprintf(&b, "    substitution: { headers: %t, query: %t, body: %t }\n",
				secret.Headers, secret.Query, secret.Body)
			b.WriteString("    require_tls_identity: true\n")
		}
	}
	return []byte(b.String())
}

// mount is the workspace mount: the folder at its own path, so that a path in the model's
// world and in the user's world are the same string.
func (p Policy) mount(o owner) string {
	spec := p.Workspace + ":" + p.Workspace
	if o.mapFiles {
		spec += fmt.Sprintf(":uid=%d,gid=%d", o.uid, o.gid)
	}
	return spec
}

// yamlString quotes a scalar only where YAML would otherwise read it as something else: a
// mount is full of colons and none of them need quoting, but a folder called "a: b" does,
// and the engine refuses a policy it cannot parse rather than guessing.
func yamlString(value string) string {
	if value == "" || needsQuotes(value) {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
	}
	return value
}

func needsQuotes(value string) bool {
	if strings.TrimSpace(value) != value {
		return true
	}
	if strings.ContainsAny(value, "\n\t\r") {
		return true
	}
	// A colon or a hash starts something else only when a space follows it, and an
	// indicator only when the scalar starts with it.
	if strings.Contains(value, ": ") || strings.Contains(value, " #") || strings.HasSuffix(value, ":") {
		return true
	}
	return strings.ContainsAny(value[:1], "-?:,[]{}#&*!|>'\"%@`")
}

// Egress needs nothing of the machine: the microVM's network is user-mode and internal, so a
// workspace with its network on always has one.
func (m *Microsandbox) Egress() error { return nil }

// Nesting reports whether a sandbox here could nest virtual machines. Two things have to
// agree: the engine has to allow nesting on its side and the machine has to be able to
// grant it (nested virtualization on the host). On the pinned 0.7.6 there is no switch
// to offer — its guests nest by default whenever the machine can — so the host half is
// the whole answer there; remove that branch when the pin moves to the release carrying
// `msb create --nested-virt`.
func (m *Microsandbox) Nesting(ctx context.Context) NestingSupport {
	engine := m.engineNesting(ctx)
	if engine.Available {
		return m.hostNesting()
	}
	if m.Installed() && Version == "0.7.6" {
		return m.hostNesting()
	}
	return engine
}

// engineNesting answers the engine half: whether this build of it offers nesting at all. It
// is asked once and kept — except while the engine is not installed yet, which is a state
// the first workspace to need it changes.
func (m *Microsandbox) engineNesting(ctx context.Context) NestingSupport {
	if !m.Installed() {
		return unavailable("the engine is not installed")
	}
	m.nestingMu.Lock()
	defer m.nestingMu.Unlock()
	if m.engine == nil {
		answer := m.detectEngineNesting(ctx)
		m.engine = &answer
	}
	return *m.engine
}

func (m *Microsandbox) detectEngineNesting(ctx context.Context) NestingSupport {
	help, err := m.run(ctx, "create", "--help")
	if err != nil {
		return unavailable("cannot ask engine %s whether it can nest: %v", Version, err)
	}
	if !strings.Contains(help, "--nested-virt") {
		return unavailable("engine %s has no nesting switch: its guests cannot nest", Version)
	}
	return NestingSupport{Available: true}
}

// hostNesting is the machine half, with the test seam applied.
func (m *Microsandbox) hostNesting() NestingSupport {
	if m.hostNested != nil {
		return m.hostNested()
	}
	return hostNestedSupport()
}
func (m *Microsandbox) Create(ctx context.Context, policy Policy, policyDir string) error {
	if strings.TrimSpace(policyDir) == "" {
		return errors.New("sandbox: no directory for the policy file")
	}
	if len(m.StateDir()) > statePathLimit {
		return fmt.Errorf(
			"%w: the sandbox state folder is %d characters (%s) and the engine's sockets need %d or fewer, so acornd cannot be installed this deep: use a shorter --data-dir",
			ErrUnavailable, len(m.StateDir()), m.StateDir(), statePathLimit,
		)
	}
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(policyDir, "sandbox.yaml")
	if err := os.WriteFile(path, policy.Render(), 0o644); err != nil {
		return err
	}
	args := []string{"create", "--conf", path, "--name", Name(policy.Workspace), "--label", "acornd"}
	// The engine is told about nesting only if it knows the flag, but then it is told either
	// way: allowed and possible is `--nested-virt`, refused is `--nested-virt=false` —
	// explicit, so that a default in the engine's own configuration cannot overrule the
	// workspace. An engine without the flag has nothing to hear. (On the pinned 0.7.6 a
	// refusal cannot be honored — there is no off switch — which the ADR owns as an interim
	// limitation: the sandbox still boots.)
	args = append(args, networkArgs(policy)...)
	if engine := m.engineNesting(ctx); engine.Available {
		if policy.DisableVMNesting {
			args = append(args, "--nested-virt=false")
		} else if m.hostNesting().Available {
			args = append(args, "--nested-virt")
		}
	}
	_, err := m.runEnv(ctx, secretEnvValues(policy.Secrets), args...)
	return err
}

// networkArgs is what a workspace may reach, said out loud for every case.
//
// The engine's implicit default is public-only, and it includes a DNS allowance that disappears the
// moment any rule is written — so inheriting it is wrong twice over: nothing of ours says "public
// only", and an engine release that changes that default would open every microVM workspace
// without a line of this changing. A rule set that forgets DNS breaks name resolution, which is
// what a naive `allow@public` does.
//
// So: the default is deny, public and DNS are allowed, and the private grant is the only thing
// added for a workspace that has one. Egress off is the rendered `network: none`, which is a whole
// policy on its own.
func networkArgs(policy Policy) []string {
	if !policy.Egress {
		return nil
	}
	args := []string{
		"--net-default", "deny",
		"--net-rule", "allow@public",
		"--net-rule", "allow@dns",
	}
	if policy.PrivateEgress {
		args = append(args, "--net-rule", "allow@private")
	}
	return args
}

func (m *Microsandbox) Start(ctx context.Context, name string, secrets []Secret) error {
	// The stored config references a host variable per secret. The engine reads it at start,
	// so the values ride the start process's environment, never the config.
	_, err := m.runEnv(ctx, startSecretEnv(secrets), "start", name)
	return err
}

func (m *Microsandbox) Stop(ctx context.Context, name string) error {
	_, err := m.run(ctx, "stop", name)
	return err
}

// Remove forces, so that removal does not depend on the sandbox being down first.
func (m *Microsandbox) Remove(ctx context.Context, name string) error {
	_, err := m.run(ctx, "rm", "--force", name)
	return err
}

// List asks the engine for every sandbox it knows. The structured form is used because the
// human table is not a contract, and because `status` reports a missing sandbox as success,
// which is a trap to build on.
func (m *Microsandbox) List(ctx context.Context) ([]Instance, error) {
	out, err := m.run(ctx, "list", "--format", "json")
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, nil
	}
	var instances []Instance
	if err := json.Unmarshal([]byte(trimmed), &instances); err != nil {
		return nil, fmt.Errorf("%w: cannot read the engine's sandbox list: %w", ErrUnavailable, err)
	}
	return instances, nil
}

// run drives the engine. The environment is set here rather than inherited so that the
// engine's store is inside the installation folder whatever the user's shell says — and so
// that the worker, which the SDK attaches with, looks in the same place.
func (m *Microsandbox) run(ctx context.Context, args ...string) (string, error) {
	return m.runEnv(ctx, nil, args...)
}

// runEnv is run with extra environment entries, last wins. It is how a secret value reaches
// the engine without being written to the policy file or passed in argv.
func (m *Microsandbox) runEnv(ctx context.Context, extra []string, args ...string) (string, error) {
	if m.runner != nil {
		if runner, ok := m.runner.(EnvRunner); ok {
			return runner.RunEnv(ctx, extra, args...)
		}
		return m.runner.Run(ctx, args...)
	}
	if !m.Installed() {
		return "", fmt.Errorf("%w: %s is missing", ErrUnavailable, m.binary())
	}
	if err := os.MkdirAll(m.StateDir(), 0o755); err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, m.binary(), args...)
	command.Env = mergeEnv(os.Environ(), m.Env(), extra)
	output, commandErr := command.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if commandErr != nil {
		if text == "" {
			text = commandErr.Error()
		}
		return text, fmt.Errorf("sandbox engine %s: %s", strings.Join(args, " "), firstLine(text))
	}
	return text, nil
}

// ArtifactFor names the release asset for a platform, or "" when the release does not
// publish one. The release uses Unix names for both fields, which is why they are mapped
// rather than passed through.
func ArtifactFor(goos, goarch string) string {
	var arch string
	switch goarch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	default:
		return ""
	}
	switch goos {
	case "linux", "darwin":
		return fmt.Sprintf("microsandbox-%s-%s.tar.gz", goos, arch)
	default:
		return ""
	}
}

// layOut arranges the release, which is published flat, the way the engine expects to be
// run: the binary in bin/, the guest kernel library in lib/ under the names it is loaded by.
// Getting this wrong is the difference between an engine that starts and one that reports a
// missing shared library, so the bundle is required to hold exactly one of each.
func layOut(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	library, err := bundleParts(entries)
	if err != nil {
		return err
	}
	if err := installBinary(dir); err != nil {
		return err
	}
	if err := installLibrary(dir, library); err != nil {
		return err
	}
	pruneBundle(dir, entries)
	return nil
}

// bundleParts is what a release bundle has to contain: the CLI, and exactly one guest kernel
// library. The release ships one per platform, so two means this is not a bundle this build
// knows how to lay out.
func bundleParts(entries []os.DirEntry) (string, error) {
	var library string
	binarySeen := false
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case name == "msb":
			binarySeen = true
		case libraryName(name):
			if library != "" {
				return "", fmt.Errorf("%w: the release bundle holds more than one libkrunfw", ErrUnavailable)
			}
			library = name
		}
	}
	if !binarySeen {
		return "", fmt.Errorf("%w: the release bundle has no msb", ErrUnavailable)
	}
	if library == "" {
		return "", fmt.Errorf("%w: the release bundle has no libkrunfw for %s", ErrUnavailable, runtime.GOOS)
	}
	return library, nil
}

// installBinary puts the CLI where the engine expects it, and makes the SDK's entry point an
// alias: the SDK and the CLI are the same program.
func installBinary(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		return err
	}
	binary := filepath.Join(dir, "bin", "msb")
	if err := os.Rename(filepath.Join(dir, "msb"), binary); err != nil {
		return err
	}
	if err := os.Chmod(binary, 0o755); err != nil {
		return err
	}
	return os.Symlink("msb", filepath.Join(dir, "bin", "microsandbox"))
}

// installLibrary puts the guest kernel library in lib/, with the names the dynamic loader looks
// for pointing at the real file.
func installLibrary(dir, library string) error {
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(dir, library), filepath.Join(dir, "lib", library)); err != nil {
		return err
	}
	for link, target := range loadNames(library) {
		if err := os.Symlink(target, filepath.Join(dir, "lib", link)); err != nil {
			return err
		}
	}
	return nil
}

// pruneBundle removes what is left: the other platforms' copies.
func pruneBundle(dir string, entries []os.DirEntry) {
	for _, entry := range entries {
		name := entry.Name()
		if name == "bin" || name == "lib" {
			continue
		}
		// What is left is the other platforms' copies of a bundle that was unpacked read-only, and
		// nothing runs before the rename that would notice a failure here.
		_ = os.RemoveAll(filepath.Join(dir, name))
	}
}

// libraryName reports whether a bundle member is the guest kernel library. The release
// contains one per platform; only ours is kept.
func libraryName(name string) bool {
	return strings.HasPrefix(name, "libkrunfw.so.") || (strings.HasPrefix(name, "libkrunfw.") && strings.HasSuffix(name, ".dylib"))
}

// loadNames are the names the binary asks the loader for: the ABI soname, and the bare name
// the SDK's entry point uses.
func loadNames(library string) map[string]string {
	if strings.HasSuffix(library, ".dylib") {
		return map[string]string{"libkrunfw.dylib": library}
	}
	version := strings.TrimPrefix(library, "libkrunfw.so.")
	abi := version
	if before, _, ok := strings.Cut(version, "."); ok {
		abi = before
	}
	return map[string]string{"libkrunfw.so." + abi: library, "libkrunfw.so": "libkrunfw.so." + abi}
}

// fetchChecksum reads the release's checksum file and returns the one for our artifact. The
// file covers every platform, so an artifact missing from it is an error rather than a
// reason to skip verification.
func fetchChecksum(ctx context.Context, client *http.Client, url, artifact string) (string, error) {
	body, err := fetch(ctx, client, url)
	if err != nil {
		return "", err
	}
	defer body.Close()
	payload, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(string(payload), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == artifact {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("%w: %s is not in the release checksums", ErrUnavailable, artifact)
}

func fetch(ctx context.Context, client *http.Client, url string) (io.ReadCloser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, url, err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("%w: %s: %s", ErrUnavailable, url, response.Status)
	}
	return response.Body, nil
}

// unpackVerified unpacks a release tarball, refusing to write anything that would land
// outside the destination. The archive is only as trustworthy as its checksum, which is why
// both checks happen: a verified download of a hostile archive would still be hostile.
func unpackVerified(body io.Reader, want, dest string) error {
	digest := sha256.New()
	reader, err := gzip.NewReader(io.TeeReader(body, digest))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer reader.Close()
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if err := extractEntry(archive, header, dest); err != nil {
			return err
		}
	}
	if got := hex.EncodeToString(digest.Sum(nil)); want != "" && got != want {
		return fmt.Errorf("%w: checksum mismatch: got %s, want %s", ErrUnavailable, got, want)
	}
	return nil
}

// extractEntry writes one archive member. Directories, files and symlinks are the three shapes a
// release takes; anything else is skipped rather than guessed at.
func extractEntry(archive *tar.Reader, header *tar.Header, dest string) error {
	target, err := safeJoin(dest, header.Name)
	if err != nil {
		return err
	}
	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, 0o755)
	case tar.TypeReg:
		return extractFile(archive, header, target)
	case tar.TypeSymlink:
		return extractSymlink(header, target)
	}
	return nil
}

func extractFile(archive *tar.Reader, header *tar.Header, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	// The owner-write bit is added because an archive member read-only in the release would
	// otherwise leave a file this install cannot replace on the next upgrade.
	mode := header.FileInfo().Mode().Perm() | 0o200
	file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, archive); err != nil {
		// The copy's error is the one to report; the close of a file already known to be bad
		// would only say the same thing twice.
		_ = file.Close()
		return err
	}
	return file.Close()
}

func extractSymlink(header *tar.Header, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	// The link's own destination has to stay inside the tree too: a symlink to /etc is how an
	// archive reaches a path that the file entries cannot.
	if _, err := safeJoin(filepath.Dir(target), header.Linkname); err != nil {
		return err
	}
	// A later layer replaces an earlier one, and the removal is allowed to find nothing there.
	_ = os.Remove(target)
	return os.Symlink(header.Linkname, target)
}

// safeJoin resolves an archive member inside a destination, refusing absolute paths and
// escapes. A tar entry named ../../.ssh/authorized_keys is otherwise a way to write outside.
func safeJoin(dest, name string) (string, error) {
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("%w: archive member %q is absolute", ErrUnavailable, name)
	}
	target := filepath.Join(dest, name)
	relative, err := filepath.Rel(dest, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: archive member %q escapes the destination", ErrUnavailable, name)
	}
	return target, nil
}

// firstLine keeps one engine complaint rather than a wall of them: the first is the cause,
// the rest are usually consequences.
func firstLine(text string) string {
	if before, _, ok := strings.Cut(text, "\n"); ok {
		return before
	}
	return text
}
