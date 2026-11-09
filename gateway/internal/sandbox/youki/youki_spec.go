package youki

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/marcelowa/acornd/gateway/internal/sandbox"
)

// The OCI configuration is the whole of what a youki sandbox may be: capabilities, devices,
// rlimits, read-only and masked paths live in the file rather than in an argv this build makes
// up per command. The generator is pure so the three rules measured on 2026-11-09 are a table
// test rather than a live one:
//
//  1. a container that may nest carries CAP_SETFCAP, or the inner uid map is refused;
//  2. a container that may nest has no masked or read-only path under /proc, or the inner
//     procfs mount is refused;
//  3. linux.cgroupsPath is absolute, or youki picks its systemd cgroup manager and needs a
//     session bus a sandbox does not have.
//
// docs/adr/0022-youki-engine.md is the spike; issue #47 §2 is the rule and the error each one
// prevents.
const (
	// youkiOCIVersion is the runtime-spec version the generated configuration states. youki
	// 0.7.0 accepts it and its own `spec` command emits 1.0.2-dev; 1.1.0 is the version whose
	// fields this file uses.
	youkiOCIVersion = "1.1.0"

	// youkiCPUPeriod is one CPU's accounting window. Quota is CPUs × period, which is what
	// "2 CPUs" means to the kernel.
	youkiCPUPeriod = 100000

	// youkiNoFileSoft and youkiNoFileHard are the open-file ceiling the sandbox's first
	// process gets. The soft limit is the machine's common default; the hard limit is what a
	// build can raise it to without a privileged process. R8 asks for the rlimit to be in the
	// policy file rather than inherited by accident.
	youkiNoFileSoft = 1024
	youkiNoFileHard = 524288

	// guestRootHome is where the workspace's private home appears inside a youki sandbox. The
	// rootfs is always the workspace's own image, so it is the root's home on every image.
	guestRootHome = "/root"
)

// ociSpec is the part of the runtime specification this engine writes. The fields it does not
// set are the runtime's own defaults; a field this file does not name is not part of the policy.
type ociSpec struct {
	OCIVersion string     `json:"ociVersion"`
	Root       ociRoot    `json:"root"`
	Process    ociProcess `json:"process"`
	Mounts     []ociMount `json:"mounts"`
	Linux      ociLinux   `json:"linux"`
}

type ociRoot struct {
	Path     string `json:"path"`
	Readonly bool   `json:"readonly"`
}

type ociProcess struct {
	Terminal        bool            `json:"terminal"`
	User            ociUser         `json:"user"`
	Args            []string        `json:"args"`
	Env             []string        `json:"env"`
	Cwd             string          `json:"cwd"`
	NoNewPrivileges bool            `json:"noNewPrivileges"`
	Capabilities    ociCapabilities `json:"capabilities"`
	Rlimits         []ociRlimit     `json:"rlimits,omitempty"`
}

type ociUser struct {
	UID uint32 `json:"uid"`
	GID uint32 `json:"gid"`
}

// ociCapabilities is the three sets the kernel check reads. A container that may nest needs
// CAP_SETFCAP in all three: verify_root_map() refuses a uid map that names the parent's uid 0
// unless the writer holds the capability, and a process that execs keeps what is in its bounding
// set. The inheritable and ambient sets are deliberately absent: nothing here needs them, and an
// empty set is the least a sandbox can be given.
type ociCapabilities struct {
	Bounding  []string `json:"bounding"`
	Effective []string `json:"effective"`
	Permitted []string `json:"permitted"`
}

type ociRlimit struct {
	Type string `json:"type"`
	Hard uint64 `json:"hard"`
	Soft uint64 `json:"soft"`
}

type ociMount struct {
	Destination string   `json:"destination"`
	Type        string   `json:"type"`
	Source      string   `json:"source"`
	Options     []string `json:"options"`
}

type ociLinux struct {
	CgroupsPath   string         `json:"cgroupsPath,omitempty"`
	UIDMappings   []ociIDMapping `json:"uidMappings,omitempty"`
	GIDMappings   []ociIDMapping `json:"gidMappings,omitempty"`
	Namespaces    []ociNamespace `json:"namespaces"`
	Resources     *ociResources  `json:"resources,omitempty"`
	MaskedPaths   []string       `json:"maskedPaths"`
	ReadonlyPaths []string       `json:"readonlyPaths"`
	Devices       []ociDevice    `json:"devices,omitempty"`
}

type ociIDMapping struct {
	ContainerID uint32 `json:"containerID"`
	HostID      uint32 `json:"hostID"`
	Size        uint32 `json:"size"`
}

type ociNamespace struct {
	Type string `json:"type"`
}

type ociResources struct {
	Memory *ociMemory `json:"memory,omitempty"`
	CPU    *ociCPU    `json:"cpu,omitempty"`
}

type ociMemory struct {
	Limit int64 `json:"limit"`
	// Swap is the total memory plus swap the cgroup may use, which is the OCI meaning of the
	// field. Setting it equal to Limit leaves no swap to absorb an allocation: without it a
	// 400 MB allocation in a 256 M workspace can land in swap and survive, which ADR 0023
	// measured as the reason a systemd MemoryMax alone did not bind.
	Swap int64 `json:"swap,omitempty"`
}

type ociCPU struct {
	Quota  int64  `json:"quota"`
	Period uint64 `json:"period"`
}

type ociDevice struct {
	Path     string  `json:"path"`
	Type     string  `json:"type"`
	Major    int64   `json:"major"`
	Minor    int64   `json:"minor"`
	FileMode *uint32 `json:"fileMode,omitempty"`
	UID      *uint32 `json:"uid,omitempty"`
	GID      *uint32 `json:"gid,omitempty"`
}

// youkiSpec turns one workspace's record into the runtime's configuration. name is the sandbox's
// engine-level name, which is also the container id and the last part of its cgroupsPath; it is
// passed rather than read from the record because the record is named by it.
func youkiSpec(config youkiConfig, name string) (ociSpec, error) {
	if err := validateYoukiRecord(config, name); err != nil {
		return ociSpec{}, err
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 0 || gid < 0 {
		return ociSpec{}, fmt.Errorf("%w: cannot map a user id on this platform, and a youki sandbox maps one", sandbox.ErrUnavailable)
	}
	resources, err := youkiResources(config)
	if err != nil {
		return ociSpec{}, err
	}
	capabilities := []string{}
	if !config.DisableNesting {
		// A leaf keeps no capability: it has no inner uid map to write, and the masks below are
		// the hardening R8 asks for. The nesting case is the one the kernel check is about.
		capabilities = []string{"CAP_SETFCAP"}
	}
	masked, readonly := youkiMasks(!config.DisableNesting)
	namespaces := []ociNamespace{
		{Type: "pid"},
		{Type: "ipc"},
		{Type: "uts"},
		{Type: "mount"},
		{Type: "user"},
	}
	if config.Network.Mode != "pasta" {
		// Egress on: the container joins pasta's network namespace, so the spec omits the
		// network namespace entirely. Egress off: an empty namespace, which is bwrap's
		// --unshare-net by another name.
		namespaces = append(namespaces, ociNamespace{Type: "network"})
	}
	return ociSpec{
		OCIVersion: youkiOCIVersion,
		Root:       ociRoot{Path: config.Rootfs, Readonly: false},
		Process: ociProcess{
			Terminal: false,
			User:     ociUser{UID: 0, GID: 0},
			Args:     youkiArgs(config),
			Env:      youkiProcessEnv(),
			Cwd:      "/",
			// Nothing in the sandbox may gain a privilege from a file's setuid bit or a file
			// capability; the two engines' boundary is namespaces and this is the part the
			// runtime can add to it for free.
			NoNewPrivileges: true,
			Capabilities: ociCapabilities{
				Bounding:  capabilities,
				Effective: capabilities,
				Permitted: capabilities,
			},
			Rlimits: []ociRlimit{{
				Type: "RLIMIT_NOFILE",
				Hard: youkiNoFileHard,
				Soft: youkiNoFileSoft,
			}},
		},
		Mounts: youkiMounts(config),
		Linux: ociLinux{
			CgroupsPath:   config.CgroupPath,
			UIDMappings:   []ociIDMapping{{ContainerID: 0, HostID: uint32(uid), Size: 1}},
			GIDMappings:   []ociIDMapping{{ContainerID: 0, HostID: uint32(gid), Size: 1}},
			Namespaces:    namespaces,
			Resources:     resources,
			MaskedPaths:   masked,
			ReadonlyPaths: readonly,
			Devices:       config.DeviceNodes,
		},
	}, nil
}

// validateYoukiRecord refuses a record that cannot become a sandbox before a file is written.
// The paths are absolute because the runtime reads them from its own root and a relative one
// would resolve somewhere nobody chose.
func validateYoukiRecord(config youkiConfig, name string) error {
	for _, check := range []struct {
		bad bool
		err error
	}{
		{strings.TrimSpace(name) == "", errors.New("sandbox: the youki sandbox has no name, so its cgroupsPath cannot be absolute")},
		{strings.Contains(name, "/"), fmt.Errorf("sandbox: the youki sandbox name %q cannot be a cgroup path", name)},
		{config.Rootfs == "", errors.New("sandbox: the youki record has no rootfs: recreate the sandbox")},
		{config.Rootfs != "" && !filepath.IsAbs(config.Rootfs), fmt.Errorf("sandbox: the youki rootfs %q is not an absolute path", config.Rootfs)},
		{config.Workspace == "" || !filepath.IsAbs(config.Workspace), fmt.Errorf("sandbox: the youki record's workspace %q is not an absolute path", config.Workspace)},
		{config.DataDir == "" || !filepath.IsAbs(config.DataDir), fmt.Errorf("sandbox: the youki record's home %q is not an absolute path", config.DataDir)},
		{config.Run == "" || !filepath.IsAbs(config.Run), fmt.Errorf("sandbox: the youki record's run folder %q is not an absolute path", config.Run)},
		{config.GuestRun == "", errors.New("sandbox: the youki record has no guest run folder for the supervisor's socket")},
		{config.GuestSocket == "", errors.New("sandbox: the youki record has no guest socket for the supervisor")},
		{config.Youki == "" || !filepath.IsAbs(config.Youki), fmt.Errorf("sandbox: the youki record's runtime %q is not an absolute path", config.Youki)},
		{config.Supervisor == "" || !filepath.IsAbs(config.Supervisor), fmt.Errorf("sandbox: the youki record's supervisor %q is not an absolute path", config.Supervisor)},
		{config.CgroupPath == "" || !filepath.IsAbs(config.CgroupPath), fmt.Errorf("sandbox: the youki record's cgroup path %q is not an absolute path", config.CgroupPath)},
	} {
		if check.bad {
			return check.err
		}
	}
	return nil
}

// youkiArgs is the sandbox's first process: this binary, running the supervisor that holds the
// namespaces and runs each command. It is the same program bwrap runs, so the worker's client
// is the one that already exists.
func youkiArgs(config youkiConfig) []string {
	args := []string{sandbox.GuestSupervisor, "internal", "sandbox-supervisor", "--socket", config.GuestSocket}
	if config.IdleTimeout != "" {
		// A kept-sandbox.Alive workspace is given a timeout that never fires rather than none at all,
		// so the flag always says what the policy decided.
		args = append(args, "--idle-timeout", config.IdleTimeout)
	}
	return args
}

// youkiProcessEnv is what the sandbox's first process gets. Three variables: a path for the few
// programs it may run, the workspace's private home, and a locale. Everything the gateway holds
// stays out; a command gets the environment the worker sends it, and nothing inherited.
func youkiProcessEnv() []string {
	return []string{"PATH=" + sandbox.ProcessPath, "HOME=" + guestRootHome, "LANG=C.UTF-8"}
}

// youkiMounts is everything a command can see, in the order the runtime applies it. The base
// mounts come first; the device binds land after /dev, as bwrap orders them; and the workspace,
// its home, the socket's folder, the resolver and this binary are the binds the policy decides.
func youkiMounts(config youkiConfig) []ociMount {
	mounts := []ociMount{
		{Destination: "/proc", Type: "proc", Source: "proc", Options: []string{}},
		{Destination: "/dev", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "strictatime", "mode=755", "size=65536k"}},
		{Destination: "/dev/pts", Type: "devpts", Source: "devpts", Options: []string{"nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620"}},
		{Destination: "/dev/shm", Type: "tmpfs", Source: "shm", Options: []string{"nosuid", "noexec", "nodev", "mode=1777", "size=65536k"}},
		// mqueue needs the ipc namespace in the same clone as the user namespace; the namespaces
		// above include both, so this is the runtime's own default rather than a second rule.
		{Destination: "/dev/mqueue", Type: "mqueue", Source: "mqueue", Options: []string{"nosuid", "noexec", "nodev"}},
		{Destination: "/sys", Type: "none", Source: "/sys", Options: []string{"rbind", "nosuid", "noexec", "nodev", "ro"}},
		{Destination: "/sys/fs/cgroup", Type: "cgroup", Source: "cgroup", Options: []string{"nosuid", "noexec", "nodev", "relatime", "ro"}},
		// The workspace's root is its image; /tmp is a tmpfs so a command's scratch files do not
		// become the next command's, which is what bwrap's --tmpfs /tmp gives.
		{Destination: "/tmp", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", "mode=1777"}},
	}
	mounts = append(mounts, ociMount{
		Destination: "/etc/resolv.conf",
		Type:        "bind",
		Source:      resolverSource(config),
		Options:     []string{"rbind", "ro"},
	})
	for _, node := range config.DeviceNodes {
		// After the /dev tmpfs, so the node lands in the /dev this sandbox actually has.
		mounts = append(mounts, ociMount{Destination: node.Path, Type: "bind", Source: node.Path, Options: []string{"rbind", "rw"}})
	}
	readonly := slices.Clone(config.ReadOnly)
	slices.Sort(readonly)
	for _, path := range readonly {
		mounts = append(mounts, ociMount{Destination: path, Type: "bind", Source: path, Options: []string{"rbind", "ro"}})
	}
	if config.SecretProxy != nil {
		// After the root, so the bind lands in the image the sandbox actually has. Read-only:
		// the bundle is the install's, and a workspace that could write it could trust itself.
		mounts = append(mounts, ociMount{
			Destination: sandbox.GuestCABundle,
			Type:        "bind",
			Source:      config.SecretProxy.BundleFile,
			Options:     []string{"rbind", "ro"},
		})
	}
	return append(mounts,
		ociMount{Destination: guestRootHome, Type: "bind", Source: config.DataDir, Options: []string{"rbind", "rw"}},
		ociMount{Destination: config.Workspace, Type: "bind", Source: config.Workspace, Options: []string{"rbind", "rw"}},
		ociMount{Destination: config.GuestRun, Type: "bind", Source: config.Run, Options: []string{"rbind", "rw"}},
		ociMount{Destination: sandbox.GuestSupervisor, Type: "bind", Source: config.Supervisor, Options: []string{"rbind", "ro"}},
	)
}

// resolverSource is the file the sandbox reads for DNS: the one the driver wrote when the
// machine's own resolver is not reachable from inside, or the machine's file unchanged.
func resolverSource(config youkiConfig) string {
	if config.ResolvConf != "" {
		return config.ResolvConf
	}
	return "/etc/resolv.conf"
}

// youkiMasks is the runtime's masked and read-only path defaults, with the /proc entries removed
// when the container may nest. lock_mnt_tree() marks every non-root mount MNT_LOCKED when a mount
// namespace is copied into a new user namespace, and mnt_already_visible() then refuses the
// inner container's fresh procfs over a non-empty path: the two entries over /proc alone
// disqualify it. The /sys entries are not under /proc and stay.
func youkiMasks(mayNest bool) (masked, readonly []string) {
	masked = []string{
		"/proc/acpi",
		"/proc/asound",
		"/proc/kcore",
		"/proc/keys",
		"/proc/latency_stats",
		"/proc/timer_list",
		"/proc/timer_stats",
		"/proc/sched_debug",
		"/proc/scsi",
		"/sys/firmware",
		"/sys/devices/virtual/powercap",
	}
	readonly = []string{
		"/proc/bus",
		"/proc/fs",
		"/proc/irq",
		"/proc/sys",
		"/proc/sysrq-trigger",
	}
	if !mayNest {
		return masked, readonly
	}
	return withoutPrefix(masked, "/proc"), withoutPrefix(readonly, "/proc")
}

// withoutPrefix is the paths that are not under prefix. A path equal to the prefix counts as
// under it: a mask over /proc itself is the one that matters most.
func withoutPrefix(paths []string, prefix string) []string {
	kept := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			continue
		}
		kept = append(kept, path)
	}
	return kept
}

// youkiResources is the budget the workspace's record asks for. A zero value is no limit, which
// is what a record that predates the policy or a test that only asks about mounts gets.
func youkiResources(config youkiConfig) (*ociResources, error) {
	resources := &ociResources{}
	if config.Memory != "" {
		limit, err := parseMemoryBytes(config.Memory)
		if err != nil {
			return nil, err
		}
		if limit > 0 {
			resources.Memory = &ociMemory{Limit: limit, Swap: limit}
		}
	}
	if config.CPUs > 0 {
		resources.CPU = &ociCPU{Quota: int64(config.CPUs) * youkiCPUPeriod, Period: youkiCPUPeriod}
	}
	if resources.Memory == nil && resources.CPU == nil {
		return nil, nil
	}
	return resources, nil
}

// parseMemoryBytes reads a policy's memory size. The suffixes are the binary ones the policy's
// own default uses: 2G is 2147483648, not 2000000000. A unit this build does not know is a
// refusal rather than a guess, because the guess would be a budget nobody chose.
func parseMemoryBytes(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}
	digits := memoryDigits(trimmed)
	if digits == 0 {
		return 0, badMemorySize(value)
	}
	number, err := strconv.ParseInt(trimmed[:digits], 10, 64)
	if err != nil || number <= 0 {
		return 0, badMemorySize(value)
	}
	multiplier, ok := memoryMultiplier(strings.ToLower(strings.TrimSpace(trimmed[digits:])))
	if !ok {
		return 0, badMemorySize(value)
	}
	if number > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("sandbox: memory %q is larger than a byte count can hold", value)
	}
	return number * multiplier, nil
}

// memoryDigits is the length of the number at the front of a size. A size that does not start
// with one has no number to read.
func memoryDigits(value string) int {
	digits := 0
	for digits < len(value) && value[digits] >= '0' && value[digits] <= '9' {
		digits++
	}
	return digits
}

// memoryMultiplier is the binary suffix a size carries. A unit this build does not know is a
// refusal rather than a guess: the guess would be a budget nobody chose.
func memoryMultiplier(unit string) (int64, bool) {
	switch unit {
	case "", "b":
		return 1, true
	case "k", "kb", "kib":
		return 1 << 10, true
	case "m", "mb", "mib":
		return 1 << 20, true
	case "g", "gb", "gib":
		return 1 << 30, true
	}
	return 0, false
}

func badMemorySize(value string) error {
	return fmt.Errorf("sandbox: memory %q is not a size (use 256M, 2G, or bytes)", value)
}
