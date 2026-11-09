// Package sandboxtest holds the fakes the sandbox packages' tests share. A fake in a test file is
// invisible to another package's tests, and the youki driver's tests live in their own package.
package sandboxtest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FakeImage is a registry that answers with a minimal root, so the pull is not this suite's
// business (image_test.go covers it) and no test needs a network.
//
// The root carries this binary's own libraries when it has any: the sandbox runs this executable
// as its supervisor, and an image with no libc can only run a static one — which is exactly what a
// release build is, and exactly what a test binary is not.
func FakeImage(_ context.Context, reference, dest string, _ func(string, ...any)) error {
	if err := makeRoot(dest); err != nil {
		return err
	}
	copyLibraries(dest)
	return nil
}

// makeRoot writes the three files an image needs to look like one: a release file, a shell, and
// the folders a bind target may land in.
func makeRoot(dest string) error {
	for _, dir := range []string{filepath.Join(dest, "etc"), filepath.Join(dest, "usr", "bin"), filepath.Join(dest, "usr", "local", "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dest, "etc", "os-release"), []byte("ID=test\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dest, "usr", "bin", "sh"), []byte("#!/bin/sh\n"), 0o755)
}

// copyLibraries copies what ldd names into the image, when the test binary is dynamic. A static
// test binary has nothing to copy, and a machine without ldd gets the same image minus libc.
func copyLibraries(dest string) {
	executable, err := os.Executable()
	if err != nil {
		return
	}
	libraries, err := exec.Command("ldd", executable).Output()
	if err != nil {
		return
	}
	for field := range strings.FieldsSeq(string(libraries)) {
		if !strings.HasPrefix(field, "/") {
			continue
		}
		target := filepath.Join(dest, field)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			continue
		}
		content, err := os.ReadFile(field)
		if err != nil {
			continue
		}
		_ = os.WriteFile(target, content, 0o755)
	}
}
