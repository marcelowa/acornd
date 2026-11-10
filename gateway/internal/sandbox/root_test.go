package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/sandbox/sandboxtest"
)

// Two workspaces on one image share the base and see only their own upper. This is the whole
// point of ADR 0039: the image is unpacked once, and a workspace pays for what it changes.
func TestTwoWorkspacesShareOneImageBase(t *testing.T) {
	state := t.TempDir()
	policy := Policy{Workspace: "/work/one"}
	one, err := WorkspaceRoot(policy, state, "ws-one", "debian:stable-slim", sandboxtest.FakeImage, true)
	if err != nil {
		t.Fatalf("first root: %v", err)
	}
	two, err := WorkspaceRoot(policy, state, "ws-two", "debian:stable-slim", sandboxtest.FakeImage, true)
	if err != nil {
		t.Fatalf("second root: %v", err)
	}
	if one.Base != two.Base {
		t.Fatalf("two workspaces got different bases: %q and %q", one.Base, two.Base)
	}
	if one.Upper == two.Upper || one.Work == two.Work {
		t.Fatal("two workspaces share an upper or work dir")
	}
	// The marker and the apt line are in the upper, which is what the merged root shows.
	if _, err := os.Stat(filepath.Join(one.Upper, ".acornd-rootfs")); err != nil {
		t.Errorf("the upper has no marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(one.Upper, "etc", "apt", "apt.conf.d", "99-acornd")); err != nil {
		t.Errorf("the upper has no apt line: %v", err)
	}
	// An install in one upper is not in the other, and never in the base.
	installed := filepath.Join(one.Upper, "usr", "bin", "installed")
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("one"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(two.Upper, "usr", "bin", "installed")); !os.IsNotExist(err) {
		t.Errorf("the second workspace sees the first's install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(one.Base, "usr", "bin", "installed")); !os.IsNotExist(err) {
		t.Errorf("the shared base was written: %v", err)
	}
}

// The fallback is the code that ran before the overlay: a real copy, with the marker and the apt
// line in it, so an old kernel loses the disk win and nothing else.
func TestAWorkspaceWithoutOverlayKeepsTheCopy(t *testing.T) {
	root, err := WorkspaceRoot(Policy{Workspace: "/work/one"}, t.TempDir(), "ws-one", "debian:stable-slim", sandboxtest.FakeImage, false)
	if err != nil {
		t.Fatalf("copy root: %v", err)
	}
	if root.Mode != "copy" || root.Rootfs == "" || root.Base != "" || root.Upper != "" {
		t.Fatalf("copy root = %+v", root)
	}
	if _, err := os.Stat(filepath.Join(root.Rootfs, ".acornd-rootfs")); err != nil {
		t.Errorf("the copy has no marker: %v", err)
	}
}

// The image roots a trust bundle is built from live in the lower on an overlay, and in the copy
// when there is no overlay. Reading the upper would miss a CA the image shipped.
func TestAnOverlayTrustsTheImagesRoots(t *testing.T) {
	state := t.TempDir()
	root, err := WorkspaceRoot(Policy{Workspace: "/work/one"}, state, "ws-one", "debian:stable-slim", sandboxtest.FakeImage, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := root.ImageRootsPath(); got != root.Base {
		t.Errorf("overlay roots = %q, want the base %q", got, root.Base)
	}
	copyRoot, err := WorkspaceRoot(Policy{Workspace: "/work/two"}, state, "ws-two", "debian:stable-slim", sandboxtest.FakeImage, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := copyRoot.ImageRootsPath(); got != copyRoot.Rootfs {
		t.Errorf("copy roots = %q, want the copy %q", got, copyRoot.Rootfs)
	}
}
