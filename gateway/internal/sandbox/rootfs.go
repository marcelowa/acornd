package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The workspace's own copy of its image, which both container engines root a sandbox at: bwrap
// binds it as `/`, and youki names it in the OCI configuration's root.path. The copy is the
// whole of what an install inside a sandbox touches, which is why it is a copy rather than a
// shared read-only tree with a layer on top: an ordinary write to an ordinary directory needs no
// privileged mount, no overlay, and no cleanup beyond removing the folder. ADR 0039 is the
// overlay this is the baseline for.

// WorkspaceRootfs returns the workspace's own copy of its image, making it on first use.
func WorkspaceRootfs(policy Policy, state, name, image string, pull ImageSource) (string, error) {
	rootfs := filepath.Join(state, name, "rootfs")
	if info, err := os.Stat(filepath.Join(rootfs, ".acornd-rootfs")); err == nil && !info.IsDir() {
		return rootfs, nil
	}
	// The workspace's own copy is the slow part of a first start — 84 MB of image, more after the
	// project's tools — so the two steps that take the time are named as they happen.
	policy.Stage("preparing the image")
	base, err := ImageBase(state, image, pull)
	if err != nil {
		return "", err
	}
	// Replaced whole, so a half-copied tree cannot be mistaken for a finished one.
	policy.Stage("copying the image")
	_ = os.RemoveAll(rootfs)
	if err := copyTree(base, rootfs); err != nil {
		return "", fmt.Errorf("sandbox: copying the image for this workspace: %w", err)
	}
	if err := os.WriteFile(filepath.Join(rootfs, ".acornd-rootfs"), []byte(image+"\n"), 0o644); err != nil {
		return "", err
	}
	// apt's own sandbox drops to _apt with setgroups, which a single-mapping user namespace
	// denies; as root inside a root that is the workspace's own, dropping is not needed.
	aptDir := filepath.Join(rootfs, "etc", "apt", "apt.conf.d")
	if err := os.MkdirAll(aptDir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(aptDir, "99-acornd"), []byte("APT::Sandbox::User \"root\";\n"), 0o644)
	}
	return rootfs, nil
}

// imageBase is the shared unpacked image, fetched once per digest and reused by every
// workspace. It is read-only in practice: a workspace copies it before it runs anything.
func ImageBase(state, image string, pull ImageSource) (string, error) {
	if strings.TrimSpace(image) == "" {
		image = DefaultImage
	}
	ref, err := parseImageReference(image)
	if err != nil {
		return "", fmt.Errorf("sandbox: %w", err)
	}
	key := slugify(ref.host+"-"+ref.repository) + "-" + slugify(ref.tag)
	dir := filepath.Join(state, "images", key)
	base := filepath.Join(dir, "base")
	complete := filepath.Join(dir, "complete")
	if imageReady(complete) {
		return base, nil
	}
	if err := pullToDir(dir, base, complete, image, pull); err != nil {
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
func pullToDir(dir, base, complete, image string, pull ImageSource) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return lockFile(filepath.Join(dir, "lock"), func() error {
		if imageReady(complete) {
			return nil
		}
		if pull == nil {
			return errors.New("sandbox: no image source is configured")
		}
		staging, err := os.MkdirTemp(dir, "base.partial-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(staging) }()
		if err := pull(context.Background(), image, staging, nil); err != nil {
			return fmt.Errorf("sandbox: fetching the image %s: %w", image, err)
		}
		_ = os.RemoveAll(base)
		if err := os.Rename(staging, base); err != nil {
			return err
		}
		return os.WriteFile(complete, []byte(image+"\n"), 0o644)
	})
}

// Root is how one workspace's root is provided: a copy of the image (the baseline), or an upper
// over the shared image base (ADR 0039). The mode is recorded so the sandbox can be started from
// the same shape later, and so a person can see which one a workspace got.
type Root struct {
	Mode   string `json:"mode"`
	Base   string `json:"base,omitempty"`
	Rootfs string `json:"rootfs,omitempty"`
	Upper  string `json:"upper,omitempty"`
	Work   string `json:"work,omitempty"`
}

// ImageRootsPath is the tree whose trust store belongs to this root: the image base for an
// overlay, and the workspace's copy otherwise.
func (r Root) ImageRootsPath() string {
	if r.Mode == "overlay" {
		return r.Base
	}
	return r.Rootfs
}

// WorkspaceRoot returns the root a workspace should run on: a shared image base with a private
// upper and work dir when overlay is true, and the workspace's own copy when it is not. The copy is
// the fallback and the code that ran before ADR 0039; a machine whose kernel, filesystem or
// bubblewrap cannot overlay loses the disk win and nothing else.
func WorkspaceRoot(policy Policy, state, name, image string, pull ImageSource, overlay bool) (Root, error) {
	if !overlay {
		rootfs, err := WorkspaceRootfs(policy, state, name, image, pull)
		if err != nil {
			return Root{}, err
		}
		return Root{Mode: "copy", Rootfs: rootfs}, nil
	}
	policy.Stage("preparing the image")
	base, err := ImageBase(state, image, pull)
	if err != nil {
		return Root{}, err
	}
	return prepareOverlay(policy, state, name, image, base)
}

// prepareOverlay makes the upper and work dir for one workspace, or finds them already made. The
// marker is written last, so an upper without it is half-provisioned and is replaced whole; the
// base is never touched.
func prepareOverlay(policy Policy, state, name, image, base string) (Root, error) {
	dir := filepath.Join(state, name)
	upper := filepath.Join(dir, "upper")
	work := filepath.Join(dir, "work")
	marker := filepath.Join(upper, ".acornd-rootfs")
	if info, err := os.Stat(marker); err == nil && !info.IsDir() {
		return Root{Mode: "overlay", Base: base, Upper: upper, Work: work}, nil
	}
	policy.Stage("preparing the overlay")
	_ = os.RemoveAll(upper)
	_ = os.RemoveAll(work)
	if err := os.MkdirAll(upper, 0o755); err != nil {
		return Root{}, err
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return Root{}, err
	}
	if err := os.WriteFile(marker, []byte(image+"\n"), 0o644); err != nil {
		return Root{}, err
	}
	// apt's own sandbox drops to _apt with setgroups, which a single-mapping user namespace
	// denies; as root inside a root that is the workspace's own, dropping is not needed. The
	// file lands in the upper and the overlay shows it at the image's path.
	aptDir := filepath.Join(upper, "etc", "apt", "apt.conf.d")
	if err := os.MkdirAll(aptDir, 0o755); err != nil {
		return Root{}, err
	}
	if err := os.WriteFile(filepath.Join(aptDir, "99-acornd"), []byte("APT::Sandbox::User \"root\";\n"), 0o644); err != nil {
		return Root{}, err
	}
	return Root{Mode: "overlay", Base: base, Upper: upper, Work: work}, nil
}
