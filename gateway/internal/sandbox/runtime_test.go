package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A socket has to live at a short path wherever the install lives, so it is not in the store: the
// folder is derived from the session, and the install only decides which socket inside it.
func TestTheSocketLivesInTheRuntimeFolderNotTheStore(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "run"))
	store := t.TempDir()
	driver := NewBwrap(store)
	first, err := driver.socketDir("/work/one")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, os.Getenv("XDG_RUNTIME_DIR")) {
		t.Fatalf("the socket folder should be under XDG_RUNTIME_DIR, got %s", first)
	}
	if strings.Contains(first, store) {
		t.Fatalf("the socket folder should not be in the store, got %s", first)
	}
	// Two workspaces do not share a socket; two installs do not either.
	second, err := driver.socketDir("/work/two")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two workspaces got the same socket folder")
	}
	other := NewBwrap(t.TempDir())
	again, err := other.socketDir("/work/one")
	if err != nil {
		t.Fatal(err)
	}
	if again == first {
		t.Fatal("two installs got the same socket folder for one path")
	}
}

// Without a session folder the fallback is still this user's own, and short enough for a socket.
func TestTheSocketFolderHasAShortFallback(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	dir, err := RuntimeDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(filepath.Join(dir, "01234567", "s.sock")) > socketLimit {
		t.Fatalf("the fallback is too long for a socket: %s", dir)
	}
}
