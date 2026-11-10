package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A socket has to live at a short path wherever the install lives, so it is not in the store: the
// folder is derived from the session, and the install only decides which socket inside it.
func TestTheSocketLivesInTheRuntimeFolderNotTheStore(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "run"))
	store := t.TempDir()
	first, err := socketDirFor(store, "/work/one")
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
	second, err := socketDirFor(store, "/work/two")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two workspaces got the same socket folder")
	}
	other := t.TempDir()
	again, err := socketDirFor(other, "/work/one")
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

// Two pulls of one image into one store must not both write it: the second one copies a tree that
// is being written, and a half-written image is cached as complete. This is what an end-to-end run
// with a shared store showed, so the lock is pinned here.
func TestTwoPullsOfOneImageDoNotInterleave(t *testing.T) {
	store := t.TempDir()
	image := "example.test/tool:1"
	started := make(chan struct{}, 4)
	pull := func(_ context.Context, _ string, dest string, _ func(string, ...any)) error {
		started <- struct{}{}
		// A tree with a directory deep enough that a truncating writer is visible.
		for _, dir := range []string{"usr", "usr/lib", "usr/lib64", "etc"} {
			if err := os.MkdirAll(filepath.Join(dest, dir), 0o755); err != nil {
				return err
			}
		}
		for index := range 40 {
			path := filepath.Join(dest, "usr", "lib", "file"+strconv.Itoa(index))
			if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
				return err
			}
			time.Sleep(2 * time.Millisecond)
		}
		return nil
	}
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			base, err := imageBase(store, image, pull)
			if err == nil {
				entries, readErr := os.ReadDir(filepath.Join(base, "usr", "lib"))
				if readErr != nil {
					err = readErr
				} else if len(entries) != 40 {
					err = fmt.Errorf("the cached image has %d files, want 40", len(entries))
				}
			}
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("a pull returned %v", err)
		}
	}
}

// The socket's length is a hard limit, and the failure without it is a supervisor that never
// answers. The boundary is pinned on both sides: a path exactly at the limit is accepted, one
// character over is refused.
func TestSandboxSocketPathsRefusesAPathOverTheLimit(t *testing.T) {
	dataDir := t.TempDir()
	workspace := "/work/one"
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(base, "run"))
	_, socket, err := SandboxSocketPaths(dataDir, workspace)
	if err != nil {
		t.Fatal(err)
	}
	// The socket is the session folder, a short hash, and "/s.sock". Padding the session by
	// (limit - length + 1) + len("/run") - 1 puts the socket one character over the limit; the
	// exact limit is one character less.
	pad := socketLimit - len(socket) + 1 + len("/run") - 1
	over := filepath.Join(base, strings.Repeat("x", pad))
	t.Setenv("XDG_RUNTIME_DIR", over)
	if _, _, err := SandboxSocketPaths(dataDir, workspace); err == nil {
		t.Fatalf("a socket of %d characters (limit %d) was accepted", socketLimit+1, socketLimit)
	} else if !strings.Contains(err.Error(), "too long") {
		t.Fatalf("the refusal should name the limit: %v", err)
	}
	exact := filepath.Join(base, strings.Repeat("x", pad-1))
	t.Setenv("XDG_RUNTIME_DIR", exact)
	if _, _, err := SandboxSocketPaths(dataDir, workspace); err != nil {
		t.Fatalf("a socket of exactly %d characters was refused: %v", socketLimit, err)
	}
}
