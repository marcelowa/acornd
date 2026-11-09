package sandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The runtime is faked the way bubblewrap's is: the probes read only what it prints, so the
// suite runs the same on a machine with youki and one without.
type fakeYouki struct {
	version string
	probe   error
	state   string
	calls   [][]string
}

func (f *fakeYouki) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	switch {
	case len(args) == 1 && args[0] == "--version":
		return f.version, nil
	case len(args) >= 1 && args[0] == "spec":
		return "", f.probe
	case len(args) >= 2 && args[0] == "state":
		if f.state == "" {
			return "", errors.New("no such container")
		}
		return `{"status":"` + f.state + `"}`, nil
	}
	return "", nil
}

// installFakeYouki puts an executable where Installed looks, so a test does not need the 8 MB
// release to ask a question about the record.
func installFakeYouki(t *testing.T, driver *Youki) {
	t.Helper()
	if err := os.MkdirAll(driver.InstallDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(driver.binary(), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func testYouki(t *testing.T, dataDir string) (*Youki, *fakeYouki) {
	t.Helper()
	// The socket's folder is outside the store, and a test must not write into the machine's
	// own runtime folder.
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "run"))
	runner := &fakeYouki{version: "youki version: " + youkiVersion + "\ncommit: 0.7.0-test\n"}
	driver := NewYouki(dataDir).
		WithRunner(runner).
		WithImageSource(fakeImage).
		WithDevicePaths("", "").
		WithLookPath(func(name string) (string, error) { return "/usr/bin/" + name, nil })
	driver.pasta = func(string) error { return nil }
	installFakeYouki(t, driver)
	return driver, runner
}

func TestYoukiIsAnEngineOfTheSeam(t *testing.T) {
	driver, _ := testYouki(t, t.TempDir())
	if driver.Name() != "youki" {
		t.Fatalf("name = %q", driver.Name())
	}
	if !driver.Isolated() {
		t.Fatal("a youki sandbox separates commands from the host with namespaces")
	}
	if driver.Version() != youkiVersion {
		t.Errorf("version = %q, want the pin %q", driver.Version(), youkiVersion)
	}
	if !strings.Contains(strings.Join(driver.Env(), " "), "ACORND_YOUKI_STATE="+driver.StateDir()) {
		t.Errorf("the worker has to find the records: %v", driver.Env())
	}
	// The seam can build it even though the chooser does not offer it yet: the next change is
	// the lifecycle, and a chooser entry that cannot create is a trap.
	built, err := Engine(t.TempDir(), "", "youki")
	if err != nil {
		t.Fatalf("Engine(youki): %v", err)
	}
	if built.Name() != "youki" {
		t.Errorf("Engine built %q", built.Name())
	}
}

func TestYoukiPrepareWritesTheRecordAndTheOCIConfiguration(t *testing.T) {
	dataDir := t.TempDir()
	driver, _ := testYouki(t, dataDir)
	workspace := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	skills := filepath.Join(dataDir, "skills")
	if err := os.MkdirAll(skills, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := Policy{
		Workspace: workspace,
		Image:     "debian:stable-slim",
		CPUs:      4,
		Memory:    "512M",
		Global:    []string{skills, filepath.Join(dataDir, "absent")},
	}
	if err := driver.Prepare(context.Background(), policy, t.TempDir()); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	path := driver.configPath(Name(workspace))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	var record youkiConfig
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("the record is not json: %v", err)
	}
	// The worker's socket client reads this file by key, whichever engine wrote it. A renamed
	// tag here is a client that finds nothing at the other end of a running sandbox.
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("the record is not an object: %v", err)
	}
	for _, key := range []string{"engine", "workspace", "image", "network", "rootfs", "home", "hidden", "socket", "readOnly"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("the record is missing the %q key the worker reads: %s", key, raw)
		}
	}
	if record.Engine != "youki" || record.Workspace != workspace || record.Image != "debian:stable-slim" {
		t.Fatalf("record names %q on engine %q for %q", record.Workspace, record.Engine, record.Image)
	}
	if record.Network.Mode != NetworkOffName {
		t.Errorf("network = %q, want off", record.Network.Mode)
	}
	if record.IdleTimeout != DefaultIdleTimeout {
		t.Errorf("idle timeout = %q, want %q", record.IdleTimeout, DefaultIdleTimeout)
	}
	if record.GuestSocket != guestSocketPath || record.GuestRun != guestRunDir {
		t.Errorf("the supervisor's guest paths = %q and %q", record.GuestSocket, record.GuestRun)
	}
	// Only folders that exist are mounted: a mount that does not resolve would make every
	// command fail.
	if len(record.ReadOnly) != 1 || record.ReadOnly[0] != skills {
		t.Errorf("read-only mounts = %v, want just %s", record.ReadOnly, skills)
	}
	if !strings.HasPrefix(record.DataDir, driver.StateDir()) {
		t.Errorf("home %q is not inside the engine's store %q", record.DataDir, driver.StateDir())
	}
	if !strings.HasPrefix(record.Rootfs, driver.StateDir()) {
		t.Errorf("rootfs %q is not inside the engine's store", record.Rootfs)
	}
	if info, err := os.Stat(record.Rootfs); err != nil || !info.IsDir() {
		t.Errorf("the workspace's rootfs was not copied from the image: %v", err)
	}
	// One mapped id makes apt's own sandbox user impossible, so the root carries the same
	// workaround bwrap's does.
	if _, err := os.Stat(filepath.Join(record.Rootfs, "etc", "apt", "apt.conf.d", "99-acornd")); err != nil {
		t.Errorf("the apt sandbox line was not written: %v", err)
	}
	if !strings.Contains(record.Beta, "APT::Sandbox::User") {
		t.Errorf("the record should say what the single mapping costs: %q", record.Beta)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the record is %v, and it names paths only this account may read: %v", info, err)
	}

	specRaw, err := os.ReadFile(record.Spec)
	if err != nil {
		t.Fatalf("OCI configuration: %v", err)
	}
	var spec ociSpec
	if err := json.Unmarshal(specRaw, &spec); err != nil {
		t.Fatalf("the OCI configuration is not json: %v", err)
	}
	if spec.Root.Path != record.Rootfs || spec.Root.Readonly {
		t.Errorf("root = %+v, want the workspace's writable copy %q", spec.Root, record.Rootfs)
	}
	if spec.Linux.Resources == nil || spec.Linux.Resources.Memory == nil || spec.Linux.Resources.Memory.Limit != 536870912 {
		t.Errorf("memory limit should be 512M in bytes: %+v", spec.Linux.Resources)
	}
	if spec.Linux.Resources.CPU == nil || spec.Linux.Resources.CPU.Quota != 400000 {
		t.Errorf("4 CPUs should be 400000/100000: %+v", spec.Linux.Resources.CPU)
	}
	if info, err := os.Stat(record.Spec); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("the OCI configuration is %v, and a person has to be able to read it: %v", info, err)
	}

	// Twice is the same state: the next command finds the same rootfs and the record is
	// rewritten rather than the sandbox remade.
	if err := driver.Prepare(context.Background(), policy, t.TempDir()); err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("record after two prepares: %v", err)
	}
}

func TestYoukiPrepareNamesWhatEgressNeeds(t *testing.T) {
	driver, _ := testYouki(t, t.TempDir())
	driver.look = func(name string) (string, error) {
		if name == "pasta" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + name, nil
	}
	if _, err := driver.writeConfig(Policy{Workspace: t.TempDir(), Egress: true}); err == nil || !strings.Contains(err.Error(), "apt install passt") {
		t.Fatalf("no pasta: %v, want the package that provides it", err)
	}
	// Without egress neither is needed: a sandbox with no network needs no network tools.
	if _, err := driver.writeConfig(Policy{Workspace: t.TempDir()}); err != nil {
		t.Fatalf("network off should not need pasta: %v", err)
	}
}

func TestYoukiListsItsOwnRecord(t *testing.T) {
	driver, runner := testYouki(t, t.TempDir())
	workspace := t.TempDir()
	if _, err := driver.writeConfig(Policy{Workspace: workspace}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	instances, err := driver.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(instances) != 1 || instances[0].Name != Name(workspace) || instances[0].Running() {
		t.Fatalf("a record whose runtime has no container should be listed and stopped: %+v", instances)
	}
	// The record is the truth about what exists; the runtime's state only says whether it is
	// still running, which is what makes a gateway restart an adoption rather than a cleanup.
	runner.state = "running"
	instances, err = driver.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(instances) != 1 || !instances[0].Running() {
		t.Fatalf("a record whose runtime says running should be running: %+v", instances)
	}
}

func TestYoukiRemoveKeepsTheWorkspace(t *testing.T) {
	driver, _ := testYouki(t, t.TempDir())
	workspace := t.TempDir()
	config, err := driver.writeConfig(Policy{Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Remove(context.Background(), Name(workspace)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(driver.configPath(Name(workspace))); !os.IsNotExist(err) {
		t.Fatalf("the record survived remove: %v", err)
	}
	// The socket's folder is named only by the record, so it has to be read before the record
	// goes: this is the leftover removal showed on a real install.
	if _, err := os.Stat(config.Run); !os.IsNotExist(err) {
		t.Fatalf("remove left the socket folder behind: %v", err)
	}
	if _, err := os.Stat(config.Spec); !os.IsNotExist(err) {
		t.Fatalf("remove left the OCI configuration behind: %v", err)
	}
	if _, err := os.Stat(driver.workspaceDir(Name(workspace))); !os.IsNotExist(err) {
		t.Fatalf("remove left the sandbox's bundle behind: %v", err)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("remove took the workspace with it: %v", err)
	}
	// Removing what is not there is already the wanted state.
	if err := driver.Remove(context.Background(), "acornd-absent"); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

func TestYoukiProbesNameWhatIsMissing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("youki is a Linux runtime: the probes this pins are the machine's answers on Linux")
	}
	t.Run("not installed", func(t *testing.T) {
		driver := NewYouki(t.TempDir())
		if driver.Installed() {
			t.Fatal("Installed said yes with no binary")
		}
		err := driver.Usable()
		if err == nil || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Usable should refuse a machine without the runtime: %v", err)
		}
		if !strings.Contains(err.Error(), "not installed") || !strings.Contains(err.Error(), driver.binary()) {
			t.Errorf("the refusal should name the missing binary: %v", err)
		}
	})
	t.Run("no user namespace", func(t *testing.T) {
		driver, runner := testYouki(t, t.TempDir())
		runner.probe = errors.New("exit status 1")
		err := driver.Usable()
		if err == nil || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Usable accepted a machine that refuses a user namespace: %v", err)
		}
		for _, want := range []string{"user namespace", "AppArmor", "sysctl -w kernel.apparmor_restrict_unprivileged_userns=0"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal should mention %q: %v", want, err)
			}
		}
	})
	t.Run("no pasta", func(t *testing.T) {
		driver := NewYouki(t.TempDir()).WithLookPath(func(string) (string, error) { return "", errors.New("not found") })
		if err := driver.Egress(); err == nil || !strings.Contains(err.Error(), "apt install passt") {
			t.Fatalf("egress without pasta: %v, want the package that provides it", err)
		}
	})
	t.Run("no kvm", func(t *testing.T) {
		driver, _ := testYouki(t, t.TempDir())
		support := driver.Nesting(context.Background())
		if support.Available || !strings.Contains(support.Reason, "/dev/kvm") {
			t.Fatalf("a machine without /dev/kvm cannot nest: %+v", support)
		}
	})
	t.Run("an old runtime", func(t *testing.T) {
		driver, runner := testYouki(t, t.TempDir())
		kvm := filepath.Join(t.TempDir(), "kvm")
		if err := os.WriteFile(kvm, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		driver.WithDevicePaths("", kvm)
		runner.version = "youki version: 0.6.0\ncommit: old\n"
		support := driver.Nesting(context.Background())
		if support.Available {
			t.Fatal("0.6.0 is not the release whose nesting behaviour was measured")
		}
		for _, want := range []string{"0.6.0", youkiVersion} {
			if !strings.Contains(support.Reason, want) {
				t.Errorf("the reason should name %q: %s", want, support.Reason)
			}
		}
	})
	t.Run("a runtime that cannot report its version", func(t *testing.T) {
		driver, runner := testYouki(t, t.TempDir())
		kvm := filepath.Join(t.TempDir(), "kvm")
		if err := os.WriteFile(kvm, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		driver.WithDevicePaths("", kvm)
		runner.version = "not a version"
		support := driver.Nesting(context.Background())
		if support.Available || !strings.Contains(support.Reason, "did not report") {
			t.Fatalf("an unreadable version must not be trusted with nesting: %+v", support)
		}
	})
	t.Run("nesting works", func(t *testing.T) {
		driver, _ := testYouki(t, t.TempDir())
		kvm := filepath.Join(t.TempDir(), "kvm")
		if err := os.WriteFile(kvm, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		driver.WithDevicePaths("", kvm)
		if support := driver.Nesting(context.Background()); !support.Available {
			t.Fatalf("a machine with /dev/kvm and the pinned runtime should nest: %+v", support)
		}
	})
}

// The release is vendored, so the install is the one place a checksum matters: a download that
// is not the file this build pinned is refused before it can run.
func TestYoukiInstallsThePinnedRelease(t *testing.T) {
	archive, digest := youkiReleaseTarball(t)
	artifact := youkiArtifact(runtime.GOOS, runtime.GOARCH)
	if artifact == "" {
		t.Skip("this platform has no youki release")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+artifact {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(archive)
	}))
	defer server.Close()
	driver := NewYouki(t.TempDir())
	driver.base = server.URL
	driver.checksum = func(string) string { return digest }
	if err := driver.Install(context.Background(), t.Logf); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !driver.Installed() {
		t.Fatal("the release was unpacked but Installed says no")
	}
	info, err := os.Stat(driver.binary())
	if err != nil {
		t.Fatalf("the binary: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the unpacked binary is not executable: %v", info.Mode())
	}
}

func TestYoukiRefusesAReleaseThatIsNotThePinnedOne(t *testing.T) {
	archive, _ := youkiReleaseTarball(t)
	artifact := youkiArtifact(runtime.GOOS, runtime.GOARCH)
	if artifact == "" {
		t.Skip("this platform has no youki release")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()
	driver := NewYouki(t.TempDir())
	driver.base = server.URL
	driver.checksum = func(string) string { return strings.Repeat("0", 64) }
	err := driver.Install(context.Background(), nil)
	if err == nil || !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("a download that is not the pinned file should be refused: %v", err)
	}
	if driver.Installed() {
		t.Fatal("a refused download left an install behind")
	}
	if _, err := os.Stat(driver.InstallDir() + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("the staging folder survived a failed install: %v", err)
	}
}

func TestYoukiDoesNotShipAnArtifactItCannotRun(t *testing.T) {
	for _, one := range []struct {
		goos, goarch string
		want         string
	}{
		{"linux", "amd64", "youki-" + youkiVersion + "-x86_64-musl.tar.gz"},
		{"linux", "arm64", "youki-" + youkiVersion + "-aarch64-musl.tar.gz"},
		{"darwin", "arm64", ""},
		{"linux", "riscv64", ""},
	} {
		if got := youkiArtifact(one.goos, one.goarch); got != one.want {
			t.Errorf("youkiArtifact(%s, %s) = %q, want %q", one.goos, one.goarch, got, one.want)
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if youkiSHA256(arch) == "" {
			t.Errorf("this build would fetch %s without a checksum", arch)
		}
	}
}

// youkiReleaseTarball is a release in the shape the installer expects: one executable named
// youki at the root. The digest is of the compressed archive, which is what the pin covers.
func youkiReleaseTarball(t *testing.T) ([]byte, string) {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	body := []byte("#!/bin/sh\nexit 0\n")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "youki", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(buffer.Bytes())
	return buffer.Bytes(), hex.EncodeToString(digest[:])
}
