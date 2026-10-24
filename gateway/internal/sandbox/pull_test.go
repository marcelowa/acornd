package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An image is fetched once per machine and unpacked into a folder every workspace copies from. The
// rules are about a machine that has one: a second pull finds the marker and does nothing, a pull
// that fails leaves nothing behind, and a folder that cannot be made says so before any bytes move.

func TestAnImageThatIsAlreadyThereIsNotFetchedAgain(t *testing.T) {
	engine := NewBwrap(t.TempDir())
	pulls := 0
	engine.pull = func(context.Context, string, string, func(string, ...any)) error {
		pulls++
		return nil
	}
	dir := t.TempDir()
	base := filepath.Join(dir, "base")
	complete := filepath.Join(dir, "complete")

	if err := engine.pullToDir(dir, base, complete, "debian:stable-slim"); err != nil {
		t.Fatal(err)
	}
	if pulls != 1 {
		t.Fatalf("the first pull should fetch once, got %d", pulls)
	}
	if err := engine.pullToDir(dir, base, complete, "debian:stable-slim"); err != nil {
		t.Fatal(err)
	}
	if pulls != 1 {
		t.Errorf("a second pull should find the marker and stop, got %d fetches", pulls)
	}
	// The marker names the image, so a later reader can tell which one this folder is.
	marker, err := os.ReadFile(complete)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(marker)) != "debian:stable-slim" {
		t.Errorf("marker = %q", marker)
	}
}

// A pull that fails leaves nothing that looks finished: the marker is written last, so a half
// unpacked folder can never be mistaken for an image installs run from.
func TestAFailedPullLeavesNoMarker(t *testing.T) {
	engine := NewBwrap(t.TempDir())
	engine.pull = func(_ context.Context, _ string, staging string, _ func(string, ...any)) error {
		// Half a tree, then a failure: what a dropped connection looks like.
		if err := os.WriteFile(filepath.Join(staging, "half"), []byte("x"), 0o600); err != nil {
			return err
		}
		return errors.New("the connection dropped")
	}
	dir := t.TempDir()
	base := filepath.Join(dir, "base")
	complete := filepath.Join(dir, "complete")

	err := engine.pullToDir(dir, base, complete, "debian:stable-slim")
	if err == nil {
		t.Fatal("a failed pull should be reported")
	}
	if _, statErr := os.Stat(complete); statErr == nil {
		t.Error("a failed pull must not leave a marker: the next start would copy a half image")
	}
	if _, statErr := os.Stat(base); statErr == nil {
		t.Error("a failed pull must not leave a base folder")
	}
	// And the staging folder is cleaned up: a machine does not accumulate half images.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "base.partial-") {
			t.Errorf("staging %q was left behind", entry.Name())
		}
	}
}

// With no image source configured there is nothing to pull, and saying so is better than an empty
// base folder that a workspace then copies.
func TestWithNoImageSourceAPullSaysSo(t *testing.T) {
	engine := NewBwrap(t.TempDir())
	// A build with no source configured: the default puller is what NewBwrap installs, and taking it
	// away is the state the branch exists for — never reaching for the network in a test.
	engine.pull = nil
	dir := t.TempDir()
	err := engine.pullToDir(dir, filepath.Join(dir, "base"), filepath.Join(dir, "complete"), "debian:stable-slim")
	if err == nil {
		t.Fatal("no source should be an error")
	}
	if !strings.Contains(err.Error(), "no image source") {
		t.Errorf("answered %v", err)
	}
}

// The folder for an image is named from the reference, so two images do not share one, and a
// reference this build cannot read is refused before anything is fetched.
func TestAnImageIsNamedByItsReference(t *testing.T) {
	engine := NewBwrap(t.TempDir())
	engine.pull = func(context.Context, string, string, func(string, ...any)) error { return nil }
	first, err := engine.imageBase("debian:stable-slim")
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.imageBase("golang:1.24")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Errorf("two images share the folder %q", first)
	}
	if _, err := engine.imageBase("::not a reference::"); err == nil {
		t.Error("a reference this build cannot read should be refused")
	}
}

// On a machine with /dev/kvm, the hint explains how to get into the group — or, when the account is
// already in it, that the bits are what is wrong. It is the message somebody reads after losing an
// afternoon, so it has to say one of the two.
func TestTheKVMHintSaysWhatToDo(t *testing.T) {
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("this machine has no /dev/kvm to explain")
	}
	hint := kvmHint()
	if hint == "" {
		t.Fatal("with /dev/kvm present the hint should say something")
	}
	if !strings.Contains(hint, "group") && !strings.Contains(hint, "permission bits") {
		t.Errorf("the hint should name the fix: %q", hint)
	}
}
