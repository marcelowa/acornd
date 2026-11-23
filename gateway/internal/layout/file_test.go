package layout

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// hold takes path's lock as another process would, and answers its release.
func hold(t *testing.T, path string, mode int) func() {
	t.Helper()
	held, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(held.Fd()), mode); err != nil {
		t.Fatal(err)
	}
	return func() {
		_ = syscall.Flock(int(held.Fd()), syscall.LOCK_UN)
		_ = held.Close()
	}
}

func TestAHeldLockIsRefusedAfterTheBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "people.json")
	release := hold(t, path, syscall.LOCK_EX)
	start := time.Now()
	_, err := Lock(path)
	waited := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "another acornd process has been holding "+path) {
		t.Fatalf("a held lock should be refused by name, got %v", err)
	}
	if waited < LockBudget || waited > 2*LockBudget {
		t.Errorf("it waited %s, want the budget of %s", waited, LockBudget)
	}
	// Released, the lock is free again: the refusal was about the moment, not the file.
	release()
	again, err := Lock(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again()
}

// A lock released by its holder within the budget is taken: the wait is for a write in progress.
func TestALockWaitsForAWriteInProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	release := hold(t, path, syscall.LOCK_EX)
	go func() {
		time.Sleep(100 * time.Millisecond)
		release()
	}()
	unlock, err := Lock(path)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	unlock()
}

// Readers share the lock and a writer waits for them; a held write lock keeps a reader out.
func TestReadersShareTheLockAndAWriterDoesNot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	first, err := LockShared(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LockShared(path)
	if err != nil {
		t.Fatalf("a second reader was kept out: %v", err)
	}
	second()
	writer, err := os.OpenFile(path+".lock", os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := syscall.Flock(int(writer.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("a writer took the lock while a reader held it")
	}
	first()
	if err := syscall.Flock(int(writer.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("the reader's release did not let the writer in: %v", err)
	}
}

// The release lets go of the lock: a second Lock in the same process is taken at once.
func TestTheReleaseLetsGo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.json")
	for range 2 {
		start := time.Now()
		release, err := Lock(path)
		if err != nil {
			t.Fatal(err)
		}
		if waited := time.Since(start); waited > LockBudget/2 {
			t.Fatalf("the lock waited %s: the last release did not let go", waited)
		}
		release()
	}
}

// A lock in a folder that went away is reported: making the folder again would make an empty file in it.
func TestALockInAMissingFolderIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gone", "people.json")
	if _, err := Lock(path); err == nil {
		t.Fatal("a lock in a missing folder was taken")
	}
	if _, err := os.Stat(filepath.Dir(path)); err == nil {
		t.Error("the lock made the folder")
	}
}

// A lock file that cannot be locked is an error at once, not a wait.
func TestALockFileThatIsAFolderIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "people.json")
	if err := os.Mkdir(path+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := Lock(path); err == nil {
		t.Fatal("a folder was locked as a file")
	}
	if waited := time.Since(start); waited > LockBudget/2 {
		t.Errorf("it waited %s for an error that does not pass", waited)
	}
}

func TestWriteFileReplacesTheFilePrivately(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "install")
	path := filepath.Join(dir, "remote.json")
	for _, payload := range []string{"first\n", "second\n"} {
		if err := WriteFile(path, []byte(payload)); err != nil {
			t.Fatalf("write: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != payload {
			t.Errorf("the file holds %q, want %q", got, payload)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 600", mode)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("a temp file was left behind: %v", entries)
	}
}

// A write that cannot land leaves the old file and no temp file.
func TestAFailedWriteLeavesTheFolderAsItWas(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	// A folder in the file's place: the rename cannot replace it.
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("{}\n")); err == nil {
		t.Fatal("a write over a folder reported success")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("a temp file was left behind: %v", entries)
	}
	// A folder that is a file cannot hold the temp file either.
	if err := WriteFile(filepath.Join(path, "keep", "x.json"), nil); err == nil {
		t.Fatal("a write under a file reported success")
	}
}

// flock is taken in one place. Three copies of the same bounded wait drifted in their errors and in which
// folder they made (#72); a fourth would drift again. Tests may hold a lock, the way another process would.
//
// The sandbox's pull lock is the one exception: it blocks for as long as a pull takes, which is minutes, so it
// is not this package's bounded wait, and its file is in the image cache rather than the install's files.
func TestNoOtherPackageTakesAFlock(t *testing.T) {
	root := filepath.Join("..", "..")
	allowed := map[string]bool{"internal/sandbox/lock_unix.go": true}
	files := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, _ := filepath.Rel(root, path)
		if filepath.Dir(relative) == filepath.Join("internal", "layout") || allowed[filepath.ToSlash(relative)] {
			return nil
		}
		parsed, err := parser.ParseFile(files, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			if selector, ok := node.(*ast.SelectorExpr); ok && selector.Sel.Name == "Flock" {
				t.Errorf("%s takes a flock: use layout.Lock", files.Position(selector.Pos()))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
