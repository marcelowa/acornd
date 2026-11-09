package youki

import (
	"os"
	"path/filepath"
	"testing"
)

// The parent's cgroup.subtree_control is the delegation: a controller that is already on is
// left alone, and only the missing one is written. A second probe on a machine that is already
// set up should cost a read.
func TestYoukiEnablesOnlyTheMissingControllers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cgroup.subtree_control")
	if err := os.WriteFile(path, []byte("cpu"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := enableControllers(dir, "memory", "cpu"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "+memory" {
		t.Errorf("wrote %q, want only the missing controller", raw)
	}
}

// A parent that is not a cgroup is a delegation the machine does not have, and the probe's
// caller gets the reason rather than a silent budget that does not bind.
func TestYoukiCgroupProbeNamesAMissingSubtreeControl(t *testing.T) {
	if err := enableControllers(t.TempDir(), "memory"); err == nil {
		t.Fatal("a directory without cgroup.subtree_control should be refused")
	}
}
