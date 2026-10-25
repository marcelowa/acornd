package sandbox

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A tree that cannot be written is an error, not a partial tree: a workspace whose copy of the
// image stops halfway is a filesystem that lies about what is installed, and the failure has to
// reach the caller that would otherwise start a sandbox on it.
func TestACopyThatCannotWriteReportsIt(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "bin"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	// A folder where the copy needs a file: opening it for writing fails even as root, and the
	// point is that the failure is reported rather than the entry being skipped.
	if err := os.MkdirAll(filepath.Join(dest, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(source, dest); err == nil {
		t.Fatal("a copy into a blocked path should fail")
	}
}

// copyFile is the per-file half, and the same rule holds: a target it cannot open is reported,
// rather than the file being silently absent from the tree.
func TestAFileThatCannotBeWrittenReportsIt(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "f"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(source, "f"))
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	// The destination name is a folder: opening it for writing fails even as root.
	if err := os.MkdirAll(filepath.Join(dest, "dest"), 0o755); err != nil {
		t.Fatal(err)
	}
	// And a folder that cannot be made, because a file is in its place.
	if err := os.WriteFile(filepath.Join(dest, "parent"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	in, out := rootsOf(t, source, dest)
	if err := copyFile(in, out, "dest", info); err == nil {
		t.Fatal("writing a file over a folder should fail")
	}
	if err := copyFile(in, out, "parent/f", info); err == nil {
		t.Fatal("a file where the folder should be should fail")
	}
}

// A link in the image is reproduced, not followed: an image that ships a link to a path on the
// host gets a link of the same shape in the workspace, and the host's file is left alone. The
// copy runs as the owner of the install folder, so following it would be a write outside the
// sandbox — which is the whole reason both trees are opened as roots.
func TestALinkInTheImageIsCopiedNotFollowed(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("host"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := copyTree(source, dest); err != nil {
		t.Fatal(err)
	}
	copied, err := os.Lstat(filepath.Join(dest, "escape"))
	if err != nil {
		t.Fatal(err)
	}
	if copied.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was followed and became a file")
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "host" {
		t.Fatalf("the file outside the tree changed: %q %v", data, err)
	}
}

// rootsOf opens the two trees a copy reads from and writes to, and takes them down with the test.
func rootsOf(t *testing.T, source, dest string) (in, out *os.Root) {
	t.Helper()
	in, err := os.OpenRoot(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close() })
	out, err = os.OpenRoot(dest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { out.Close() })
	return in, out
}

// A layer entry is written the same way, and the same refusal applies: an image that cannot be
// unpacked is an error from the pull, not a rootfs missing a file nobody noticed.
func TestALayerFileThatCannotBeWrittenReportsIt(t *testing.T) {
	dest := t.TempDir()
	blocked := filepath.Join(dest, "parent")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	header := &tar.Header{Name: "parent/file", Typeflag: tar.TypeReg, Mode: 0o644}
	if err := writeEntry(filepath.Join(blocked, "file"), header, strings.NewReader("x")); err == nil {
		t.Fatal("a layer entry under a file should fail")
	}
	// And a target that is itself a folder.
	folder := filepath.Join(dest, "folder")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeEntry(folder, header, strings.NewReader("x")); err == nil {
		t.Fatal("a layer entry over a folder should fail")
	}
}
