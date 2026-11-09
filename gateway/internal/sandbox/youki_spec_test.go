package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The three rules that decide whether a youki container can itself run a container are
// properties of the generated configuration, not of the machine: each was measured by its own
// error on 2026-11-09 (issue #47 §2), and each is pinned here so a runtime upgrade that changes
// its sample configuration cannot take nesting away without a test going red.

// testYoukiConfig is a complete record for a workspace with an image, a home, a socket and a
// budget. Each test changes the one field it is about.
func testYoukiConfig() youkiConfig {
	return youkiConfig{
		Engine:      "youki",
		Workspace:   "/work/ws",
		Image:       "debian:stable-slim",
		Network:     sandboxNetwork{Mode: NetworkOffName},
		Rootfs:      "/state/ws/rootfs",
		DataDir:     "/state/ws/home",
		Hidden:      "/home/me/.acornd",
		ReadOnly:    []string{"/home/me/.agents", "/install/skills"},
		ResolvConf:  "/state/ws/resolv.conf",
		Youki:       "/install/tools/youki/youki",
		Supervisor:  "/install/acornd",
		Spec:        "/state/ws/config.json",
		Socket:      "/run/acornd/abc/s.sock",
		GuestSocket: guestSocketPath,
		Run:         "/run/acornd/abc",
		GuestRun:    guestRunDir,
		IdleTimeout: DefaultIdleTimeout,
		CPUs:        DefaultCPUs,
		Memory:      DefaultMemory,
		Beta:        youkiBeta,
	}
}

func mustYoukiSpec(t *testing.T, config youkiConfig) ociSpec {
	t.Helper()
	spec, err := youkiSpec(config, "acornd-ws-12345678")
	if err != nil {
		t.Fatalf("youkiSpec: %v", err)
	}
	return spec
}

func TestYoukiSpecGivesANestingContainerSETFCAP(t *testing.T) {
	config := testYoukiConfig()
	config.DisableNesting = false
	spec := mustYoukiSpec(t, config)
	for _, set := range []struct {
		name string
		caps []string
	}{
		{"bounding", spec.Process.Capabilities.Bounding},
		{"effective", spec.Process.Capabilities.Effective},
		{"permitted", spec.Process.Capabilities.Permitted},
	} {
		if !slices.Contains(set.caps, "CAP_SETFCAP") {
			t.Errorf("%s does not carry CAP_SETFCAP, so the inner uid map is refused: %v", set.name, set.caps)
		}
	}
	// verify_root_map() checks the capability against the map's parent namespace, which is why
	// the container needs it even though its own root would not.
	if len(spec.Linux.UIDMappings) != 1 {
		t.Fatalf("the nesting rule is about writing one uid map, got %v", spec.Linux.UIDMappings)
	}
}

func TestYoukiSpecDropsProcMasksWhenNestingIsAllowed(t *testing.T) {
	config := testYoukiConfig()
	config.DisableNesting = false
	spec := mustYoukiSpec(t, config)
	for _, path := range spec.Linux.MaskedPaths {
		if path == "/proc" || strings.HasPrefix(path, "/proc/") {
			t.Errorf("a /proc mask makes the inner procfs mount EPERM: %q", path)
		}
	}
	for _, path := range spec.Linux.ReadonlyPaths {
		if path == "/proc" || strings.HasPrefix(path, "/proc/") {
			t.Errorf("a /proc read-only path makes the inner procfs mount EPERM: %q", path)
		}
	}
	// Only the /proc entries are conditional. The /sys masks are not under /proc and stay.
	if !slices.Contains(spec.Linux.MaskedPaths, "/sys/firmware") {
		t.Errorf("the /sys masks are not part of the nesting rule: %v", spec.Linux.MaskedPaths)
	}
}

func TestYoukiSpecKeepsProcMasksWhenNestingIsRefused(t *testing.T) {
	config := testYoukiConfig()
	config.DisableNesting = true
	spec := mustYoukiSpec(t, config)
	for _, want := range []string{"/proc/kcore", "/proc/keys", "/sys/firmware"} {
		if !slices.Contains(spec.Linux.MaskedPaths, want) {
			t.Errorf("a leaf workspace keeps its masks; %q is missing from %v", want, spec.Linux.MaskedPaths)
		}
	}
	for _, want := range []string{"/proc/sys", "/proc/sysrq-trigger"} {
		if !slices.Contains(spec.Linux.ReadonlyPaths, want) {
			t.Errorf("a leaf workspace keeps its read-only paths; %q is missing from %v", want, spec.Linux.ReadonlyPaths)
		}
	}
	// A leaf has no inner uid map to write, so it does not need the capability that lets it.
	if len(spec.Process.Capabilities.Bounding) != 0 {
		t.Errorf("a leaf workspace should carry no capability, got %v", spec.Process.Capabilities.Bounding)
	}
}

func TestYoukiSpecCgroupsPathIsAbsolute(t *testing.T) {
	spec := mustYoukiSpec(t, testYoukiConfig())
	if !filepath.IsAbs(spec.Linux.CgroupsPath) {
		t.Fatalf("a relative cgroupsPath selects youki's systemd manager, which needs a session bus: %q", spec.Linux.CgroupsPath)
	}
	if !strings.HasPrefix(spec.Linux.CgroupsPath, youkiCgroupRoot+"/") {
		t.Errorf("cgroupsPath %q is not under %q", spec.Linux.CgroupsPath, youkiCgroupRoot)
	}
	if !strings.Contains(spec.Linux.CgroupsPath, "acornd-ws-12345678") {
		t.Errorf("cgroupsPath %q does not name the sandbox", spec.Linux.CgroupsPath)
	}
}

func TestYoukiSpecOmitsTheNetworkNamespaceWhenEgressIsOn(t *testing.T) {
	config := testYoukiConfig()
	config.Network = sandboxNetwork{Mode: "pasta"}
	on := mustYoukiSpec(t, config)
	if slices.ContainsFunc(on.Linux.Namespaces, func(namespace ociNamespace) bool { return namespace.Type == "network" }) {
		t.Errorf("egress on must join pasta's namespace, not make an empty one: %v", on.Linux.Namespaces)
	}

	config.Network = sandboxNetwork{Mode: NetworkOffName}
	off := mustYoukiSpec(t, config)
	if !slices.ContainsFunc(off.Linux.Namespaces, func(namespace ociNamespace) bool { return namespace.Type == "network" }) {
		t.Errorf("egress off must be an empty network namespace: %v", off.Linux.Namespaces)
	}
}

func TestYoukiSpecMapsOneUserAndReportsWhatThatCosts(t *testing.T) {
	spec := mustYoukiSpec(t, testYoukiConfig())
	if len(spec.Linux.UIDMappings) != 1 || len(spec.Linux.GIDMappings) != 1 {
		t.Fatalf("a rootless youki sandbox maps one id, got %v and %v", spec.Linux.UIDMappings, spec.Linux.GIDMappings)
	}
	uid := spec.Linux.UIDMappings[0]
	if uid.ContainerID != 0 || uid.HostID != uint32(os.Getuid()) || uid.Size != 1 {
		t.Errorf("uid map = %+v, want 0 -> %d of size 1", uid, os.Getuid())
	}
	gid := spec.Linux.GIDMappings[0]
	if gid.ContainerID != 0 || gid.HostID != uint32(os.Getgid()) || gid.Size != 1 {
		t.Errorf("gid map = %+v, want 0 -> %d of size 1", gid, os.Getgid())
	}
	// The cost is not hidden: chown to another id fails under one mapping, so the record says
	// so and the rootfs carries the apt workaround (rootfs.go).
	if !strings.Contains(youkiBeta, "APT::Sandbox::User") {
		t.Errorf("the record should name the workaround the single mapping costs: %q", youkiBeta)
	}
}

// The rest of the policy, in one table: what a command sees, what the runtime creates, and what
// the budget becomes. The three tests above are the measured rules; this is the shape they sit
// in.
func TestYoukiSpecIsTheSandboxThePolicyDescribes(t *testing.T) {
	config := testYoukiConfig()
	config.Network = sandboxNetwork{Mode: "pasta", Address: sandboxAddress, Gateway: sandboxGateway, Pasta: "/usr/bin/pasta"}
	config.Devices = true
	config.DeviceNodes = []ociDevice{{Path: "/dev/net/tun", Type: "c", Major: 10, Minor: 200}}
	config.SecretProxy = &sandboxSecretProxy{
		URL: "http://acornd:token@127.0.0.1:41234", BundleFile: "/state/ws/ca-bundle.pem",
		Bundle: guestCABundle, Port: 41234,
	}
	spec := mustYoukiSpec(t, config)

	if spec.OCIVersion != youkiOCIVersion {
		t.Errorf("ociVersion = %q, want %q", spec.OCIVersion, youkiOCIVersion)
	}
	if spec.Root.Path != config.Rootfs || spec.Root.Readonly {
		t.Errorf("root = %+v, want %q and writable: an install lands there", spec.Root, config.Rootfs)
	}
	if spec.Process.User != (ociUser{UID: 0, GID: 0}) {
		t.Errorf("the sandbox runs as root inside its single mapping, got %+v", spec.Process.User)
	}
	if !spec.Process.NoNewPrivileges {
		t.Error("nothing in the sandbox may gain a privilege from a file")
	}
	if spec.Process.Cwd != "/" {
		t.Errorf("cwd must exist inside the rootfs and / always does, got %q", spec.Process.Cwd)
	}
	if len(spec.Process.Args) != 2 || spec.Process.Args[0] != guestYouki || spec.Process.Args[1] != "--version" {
		t.Errorf("step 2's first process is the runtime's version command, got %v", spec.Process.Args)
	}
	for _, want := range []string{"PATH=" + processPath, "HOME=" + guestRootHome, "LANG=C.UTF-8"} {
		if !slices.Contains(spec.Process.Env, want) {
			t.Errorf("the sandbox's own environment should carry %q: %v", want, spec.Process.Env)
		}
	}
	if strings.Contains(strings.Join(spec.Process.Env, " "), "ACORND") {
		t.Errorf("nothing of this install belongs in the sandbox's own environment: %v", spec.Process.Env)
	}
	if len(spec.Process.Rlimits) == 0 {
		t.Error("R8 wants the rlimit in the record rather than inherited by accident")
	}

	for _, one := range []struct {
		destination string
		source      string
		options     []string
		why         string
	}{
		{config.Workspace, config.Workspace, []string{"rw"}, "the workspace at its own path, read-write"},
		{guestRootHome, config.DataDir, []string{"rw"}, "the private home, where a toolchain installs"},
		{config.GuestRun, config.Run, []string{"rw"}, "the supervisor's socket folder on both sides"},
		{"/etc/resolv.conf", config.ResolvConf, []string{"ro"}, "a resolver the sandbox can reach"},
		{guestSupervisor, config.Supervisor, []string{"ro"}, "this binary, bound in for step 3"},
		{guestYouki, config.Youki, []string{"ro"}, "the runtime step 2 runs"},
		{guestCABundle, config.SecretProxy.BundleFile, []string{"ro"}, "the trust bundle, read-only"},
		{"/dev/net/tun", "/dev/net/tun", []string{"rw"}, "the device grant, after the /dev tmpfs"},
	} {
		mount, ok := youkiMountAt(spec, one.destination)
		if !ok {
			t.Errorf("no mount at %s: %s", one.destination, one.why)
			continue
		}
		if mount.Source != one.source {
			t.Errorf("mount %s comes from %q, want %q: %s", one.destination, mount.Source, one.source, one.why)
		}
		for _, option := range one.options {
			if !slices.Contains(mount.Options, option) {
				t.Errorf("mount %s should be %q: %s (%v)", one.destination, option, one.why, mount.Options)
			}
		}
	}
	for _, path := range config.ReadOnly {
		mount, ok := youkiMountAt(spec, path)
		if !ok || !slices.Contains(mount.Options, "ro") || mount.Source != path {
			t.Errorf("the global resource %s should be bound read-only at its own path: %+v", path, mount)
		}
	}
	if mount, ok := youkiMountAt(spec, "/tmp"); !ok || mount.Type != "tmpfs" {
		t.Errorf("/tmp should be a tmpfs, as it is for bwrap: %+v", mount)
	}
	if !slices.ContainsFunc(spec.Linux.Devices, func(device ociDevice) bool { return device.Path == "/dev/net/tun" }) {
		t.Errorf("the device grant should be in linux.devices: %v", spec.Linux.Devices)
	}

	// 2G and 2 CPUs are the policy's defaults, and the runtime's own units are bytes and a
	// 100 ms period.
	if spec.Linux.Resources == nil || spec.Linux.Resources.Memory == nil || spec.Linux.Resources.CPU == nil {
		t.Fatalf("the budget is missing: %+v", spec.Linux.Resources)
	}
	if got := spec.Linux.Resources.Memory.Limit; got != 2147483648 {
		t.Errorf("memory limit = %d, want 2147483648 for 2G", got)
	}
	if got := spec.Linux.Resources.Memory.Swap; got != 2147483648 {
		t.Errorf("swap = %d, want it equal to the limit so an allocation cannot land in swap", got)
	}
	if got := spec.Linux.Resources.CPU; got.Quota != 200000 || got.Period != 100000 {
		t.Errorf("cpu = %+v, want 200000/100000 for 2 CPUs", got)
	}
}

func youkiMountAt(spec ociSpec, destination string) (ociMount, bool) {
	for _, mount := range spec.Mounts {
		if mount.Destination == destination {
			return mount, true
		}
	}
	return ociMount{}, false
}

// A record that cannot be a sandbox is refused before a file is written, with the field named:
// a half-written configuration is a runtime error nobody can place.
func TestYoukiSpecRefusesARecordThatCannotBeASandbox(t *testing.T) {
	for _, one := range []struct {
		why    string
		change func(*youkiConfig)
		says   string
	}{
		{"no rootfs", func(c *youkiConfig) { c.Rootfs = "" }, "no rootfs"},
		{"a relative rootfs", func(c *youkiConfig) { c.Rootfs = "rootfs" }, "not an absolute path"},
		{"no workspace", func(c *youkiConfig) { c.Workspace = "" }, "workspace"},
		{"no home", func(c *youkiConfig) { c.DataDir = "" }, "home"},
		{"no run folder", func(c *youkiConfig) { c.Run = "" }, "run folder"},
		{"no guest run", func(c *youkiConfig) { c.GuestRun = "" }, "guest run folder"},
		{"no socket", func(c *youkiConfig) { c.GuestSocket = "" }, "guest socket"},
		{"no runtime", func(c *youkiConfig) { c.Youki = "" }, "runtime"},
		{"no supervisor", func(c *youkiConfig) { c.Supervisor = "" }, "supervisor"},
	} {
		t.Run(one.why, func(t *testing.T) {
			config := testYoukiConfig()
			one.change(&config)
			_, err := youkiSpec(config, "acornd-ws-12345678")
			if err == nil {
				t.Fatal("the record should be refused")
			}
			if !strings.Contains(err.Error(), one.says) {
				t.Errorf("the refusal should name %q: %v", one.says, err)
			}
		})
	}
	config := testYoukiConfig()
	if _, err := youkiSpec(config, "a/name"); err == nil || !strings.Contains(err.Error(), "cgroup path") {
		t.Errorf("a name with a slash should be refused: %v", err)
	}
}

func TestYoukiMemorySizesAreBytes(t *testing.T) {
	for _, one := range []struct {
		value string
		want  int64
	}{
		{"256M", 268435456},
		{"2G", 2147483648},
		{"512MiB", 536870912},
		{"1024", 1024},
		{" 128m ", 134217728},
		{"", 0},
	} {
		got, err := parseMemoryBytes(one.value)
		if err != nil {
			t.Errorf("%q: %v", one.value, err)
			continue
		}
		if got != one.want {
			t.Errorf("%q = %d, want %d", one.value, got, one.want)
		}
	}
	for _, bad := range []string{"two gigs", "0", "M", "1T"} {
		if _, err := parseMemoryBytes(bad); err == nil {
			t.Errorf("%q should be refused rather than guessed at", bad)
		}
	}
}
