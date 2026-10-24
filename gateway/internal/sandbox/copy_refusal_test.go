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
	source := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(source, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	// The destination is a folder: opening it for writing fails even as root.
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(source, dest, info); err == nil {
		t.Fatal("writing a file over a folder should fail")
	}
	// And a folder that cannot be made, because a file is in its place.
	blocked := filepath.Join(t.TempDir(), "parent")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(source, filepath.Join(blocked, "f"), info); err == nil {
		t.Fatal("a file where the folder should be should fail")
	}
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
