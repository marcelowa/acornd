//go:build linux

package sandbox

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// MountOverlay mounts the overlay a youki sandbox's root is made from. It is called by
// overlay-run in a private mount namespace before the runtime prepares the container's rootfs.
// The userxattr form is what overlayfs needs inside a user namespace; a machine that refuses it
// (a privileged mount, some kernels) gets the plain form, and both failing is the error.
func MountOverlay(base, upper, work, root string) error {
	if base == "" || upper == "" || work == "" || root == "" {
		return errors.New("sandbox: an overlay needs a base, an upper, a work dir and a root")
	}
	if _, err := os.Stat(root); err != nil {
		return fmt.Errorf("sandbox: the overlay's root %s: %w", root, err)
	}
	options := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", base, upper, work)
	err := syscall.Mount("overlay", root, "overlay", 0, options+",userxattr")
	if errors.Is(err, syscall.EINVAL) {
		err = syscall.Mount("overlay", root, "overlay", 0, options)
	}
	if errors.Is(err, syscall.EBUSY) {
		// The root is already an overlay: a second create after a crash must not fail on the
		// mount the first one made.
		return nil
	}
	if err != nil {
		return fmt.Errorf("sandbox: mounting the overlay at %s: %w", root, err)
	}
	return nil
}

// OverlayCommand is the internal hook's argv: parse the four paths and mount. A failure is the
// container's failure, never a fallback: a root that did not mount is not a sandbox.
func OverlayCommand(argv []string) error {
	flags := flag.NewFlagSet("internal overlay", flag.ContinueOnError)
	base := flags.String("base", "", "the shared image base (the lower)")
	upper := flags.String("upper", "", "this workspace's writable layer")
	work := flags.String("work", "", "overlayfs's work dir")
	root := flags.String("root", "", "the mountpoint the runtime's root.path names")
	if err := flags.Parse(argv); err != nil {
		return err
	}
	return MountOverlay(*base, *upper, *work, *root)
}

// OverlayRunCommand runs a command with an overlay mounted at root before it starts. The mount
// lives in a private mount namespace, so the host's mounts are untouched; the runtime's container
// is cloned from that namespace and keeps the overlay when this process exits. That ordering is
// the point: youki creates its default devices in the rootfs during create, so the overlay has to
// be there before create, not in a hook after it.
//
// The command is re-executed as a child in the new namespace; this process waits for it. A
// non-root caller has no CAP_SYS_ADMIN to mount with, and the driver falls back to the copy.
func OverlayRunCommand(argv []string) error {
	separator := -1
	for index, argument := range argv {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(argv) {
		return errors.New("sandbox: overlay-run needs --base/--upper/--work/--root -- <command>")
	}
	flags := flag.NewFlagSet("internal overlay-run", flag.ContinueOnError)
	base := flags.String("base", "", "the shared image base (the lower)")
	upper := flags.String("upper", "", "this workspace's writable layer")
	work := flags.String("work", "", "overlayfs's work dir")
	root := flags.String("root", "", "the mountpoint the runtime's root.path names")
	if err := flags.Parse(argv[:separator]); err != nil {
		return err
	}
	command := argv[separator+1:]
	if os.Getenv("ACORND_OVERLAY_CHILD") == "1" {
		// A mount namespace cloned from the host inherits shared propagation; without this
		// the overlay would propagate back to the host and its mountpoint could never be removed.
		if err := syscall.Mount("", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, ""); err != nil {
			return fmt.Errorf("sandbox: making the overlay mount namespace private: %w", err)
		}
		if err := MountOverlay(*base, *upper, *work, *root); err != nil {
			return err
		}
		return syscall.Exec(command[0], command, os.Environ())
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	child := exec.Command(self, append([]string{"internal", "overlay-run"}, argv...)...)
	child.Env = append(os.Environ(), "ACORND_OVERLAY_CHILD=1")
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	// A user namespace first: a normal account has no CAP_SYS_ADMIN to mount with, but the
	// namespace gives it that power inside its own bubble. The uid mapping is 0 to this account, so
	// the overlay files it creates are this account's.
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
	return child.Run()
}
