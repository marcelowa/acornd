//go:build linux

package sandbox

import (
	"os"
	"syscall"
)

// hostDeviceEntry describes a host device node the way the OCI specification wants it: the
// runtime creates the same device inside the sandbox, with the numbers the machine's own node
// carries. A path that is missing or is not a character device is not a device the sandbox can
// be given, so it is skipped rather than guessed at: a workspace on a machine without /dev/kvm
// still opens, and the engine says why it cannot nest.
func hostDeviceEntry(path string) (ociDevice, bool) {
	info, err := os.Stat(path)
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return ociDevice{}, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ociDevice{}, false
	}
	mode := uint32(info.Mode().Perm())
	// The container's uid 0 is the host user (one mapping), so the node is owned by the sandbox's
	// own root. The host's owner and group are not ids the container can name.
	owner := uint32(0)
	return ociDevice{
		Path:     path,
		Type:     "c",
		Major:    deviceMajor(stat.Rdev),
		Minor:    deviceMinor(stat.Rdev),
		FileMode: &mode,
		UID:      &owner,
		GID:      &owner,
	}, true
}

// deviceMajor and deviceMinor split a Linux dev_t the way makedev(3) packs it. The syscall
// package stopped carrying Major and Minor, and the OCI configuration needs the two numbers
// rather than the packed one.
func deviceMajor(dev uint64) int64 {
	return int64((dev>>8)&0xfff | (dev>>32)&^0xfff)
}

func deviceMinor(dev uint64) int64 {
	return int64(dev&0xff | (dev>>12)&^0xff)
}
