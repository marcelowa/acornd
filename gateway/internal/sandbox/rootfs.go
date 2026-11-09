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

// workspaceRootfs returns the workspace's own copy of its image, making it on first use.
func workspaceRootfs(policy Policy, state, name, image string, pull imageSource) (string, error) {
	rootfs := filepath.Join(state, name, "rootfs")
	if info, err := os.Stat(filepath.Join(rootfs, ".acornd-rootfs")); err == nil && !info.IsDir() {
		return rootfs, nil
	}
	// The workspace's own copy is the slow part of a first start — 84 MB of image, more after the
	// project's tools — so the two steps that take the time are named as they happen.
	policy.Stage("preparing the image")
	base, err := imageBase(state, image, pull)
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
func imageBase(state, image string, pull imageSource) (string, error) {
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
func pullToDir(dir, base, complete, image string, pull imageSource) error {
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
