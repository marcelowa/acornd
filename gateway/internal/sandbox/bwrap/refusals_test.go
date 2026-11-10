package bwrap

import (
	"github.com/marcelowa/acornd/gateway/internal/sandbox"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheStateDirectoryIsTheOneItWasGiven(t *testing.T) {
	dir := t.TempDir()
	// The engine keeps its own folder under whatever it is given, so two engines never share one.
	if got := NewBwrap("data").WithStateDir(dir).StateDir(); got != filepath.Join(dir, "bwrap") {
		t.Errorf("state dir = %q, want %q", got, filepath.Join(dir, "bwrap"))
	}
}

func TestBwrapRefusesAPolicyWithNoWorkspace(t *testing.T) {
	err := NewBwrap(t.TempDir()).validatePolicy(sandbox.Policy{})
	if err == nil {
		t.Fatal("a policy with no workspace should be refused")
	}
	// And one whose folder is not there: the sandbox mounts it, so a missing folder is a
	// sandbox that would start empty.
	err = NewBwrap(t.TempDir()).validatePolicy(sandbox.Policy{Workspace: filepath.Join(t.TempDir(), "gone")})
	if err == nil || !strings.Contains(err.Error(), "no workspace folder") {
		t.Fatalf("a missing folder should be named, got %v", err)
	}
}
