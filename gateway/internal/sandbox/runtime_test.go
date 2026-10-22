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
		for index := 0; index < 40; index++ {
			path := filepath.Join(dest, "usr", "lib", "file"+strconv.Itoa(index))
			if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
				return err
			}
			time.Sleep(2 * time.Millisecond)
		}
		return nil
	}
	one := NewBwrap(store)
	one.pull = pull
	two := NewBwrap(store)
	two.pull = pull
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for _, driver := range []*Bwrap{one, two} {
		wg.Add(1)
		go func(driver *Bwrap) {
			defer wg.Done()
			base, err := driver.imageBase(image)
			if err == nil {
				entries, readErr := os.ReadDir(filepath.Join(base, "usr", "lib"))
				if readErr != nil {
					err = readErr
				} else if len(entries) != 40 {
					err = fmt.Errorf("the cached image has %d files, want 40", len(entries))
				}
			}
			errors <- err
		}(driver)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("a pull returned %v", err)
		}
	}
}
