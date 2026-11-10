package supervise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fileAnswer sends one file verb and reads its one answer. The supervisor answers a file
// request on the same connection it arrived on, so a test needs no command loop.
func fileAnswer(t *testing.T, c *client, request Request) Event {
	t.Helper()
	c.send(t, request)
	_ = c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var event Event
	if err := c.decoder.Decode(&event); err != nil {
		t.Fatalf("%s %s: %v", request.Type, request.Path, err)
	}
	return event
}

// A file question is answered by the process inside the sandbox, not by the worker's host: the
// same bytes the sandbox wrote come back, and a missing path is an answer rather than an error.
func TestFilesAreReadWrittenAndListedByTheSandbox(t *testing.T) {
	_, socket := startServer(t)
	c := dial(t, socket)
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")

	if event := fileAnswer(t, c, Request{Type: "write", ID: "w", Path: path, Content: []byte("from the test\n")}); event.Type != "file" {
		t.Fatalf("write: %+v", event)
	}
	read := fileAnswer(t, c, Request{Type: "read", ID: "r", Path: path})
	if read.Type != "file" || string(read.Content) != "from the test\n" {
		t.Fatalf("read: %+v", read)
	}

	appendEvent := fileAnswer(t, c, Request{Type: "write", ID: "a", Path: path, Content: []byte("more\n"), Append: true})
	if appendEvent.Type != "file" {
		t.Fatalf("append: %+v", appendEvent)
	}
	read = fileAnswer(t, c, Request{Type: "read", ID: "r2", Path: path})
	if string(read.Content) != "from the test\nmore\n" {
		t.Fatalf("append changed the wrong bytes: %q", read.Content)
	}

	info := fileAnswer(t, c, Request{Type: "stat", ID: "s", Path: path})
	if info.Info == nil || info.Info.Directory || info.Info.Size != int64(len("from the test\nmore\n")) {
		t.Fatalf("stat: %+v", info)
	}

	entries := fileAnswer(t, c, Request{Type: "list", ID: "l", Path: dir})
	if len(entries.Entries) != 1 || entries.Entries[0].Name != "hello.txt" || entries.Entries[0].Directory {
		t.Fatalf("list: %+v", entries.Entries)
	}

	if event := fileAnswer(t, c, Request{Type: "rename", ID: "m", Path: path, To: filepath.Join(dir, "renamed.txt")}); event.Type != "file" {
		t.Fatalf("rename: %+v", event)
	}
	if event := fileAnswer(t, c, Request{Type: "read", ID: "r3", Path: path}); !event.Missing {
		t.Fatalf("the old name should be gone: %+v", event)
	}
	if event := fileAnswer(t, c, Request{Type: "remove", ID: "d", Path: filepath.Join(dir, "renamed.txt")}); event.Type != "file" {
		t.Fatalf("remove: %+v", event)
	}
	if event := fileAnswer(t, c, Request{Type: "stat", ID: "s2", Path: filepath.Join(dir, "renamed.txt")}); !event.Missing {
		t.Fatalf("a removed path should be missing: %+v", event)
	}
}

// Removing what is not there is the wanted state when force is set, and an error otherwise.
func TestRemoveHonoursForce(t *testing.T) {
	_, socket := startServer(t)
	c := dial(t, socket)
	missing := filepath.Join(t.TempDir(), "missing", "file")

	if event := fileAnswer(t, c, Request{Type: "remove", ID: "1", Path: missing}); event.Type != "error" {
		t.Fatalf("a plain remove of a missing path should fail: %+v", event)
	}
	if event := fileAnswer(t, c, Request{Type: "remove", ID: "2", Path: missing, Force: true}); event.Type != "file" {
		t.Fatalf("a forced remove of a missing path is already done: %+v", event)
	}
}

// A directory the tool creates may have missing parents, because that is what the tool layer
// asks for: one call, the whole path.
func TestMkdirMakesTheWholePath(t *testing.T) {
	_, socket := startServer(t)
	c := dial(t, socket)
	dir := filepath.Join(t.TempDir(), "one", "two", "three")

	if event := fileAnswer(t, c, Request{Type: "mkdir", ID: "m", Path: dir}); event.Type != "file" {
		t.Fatalf("mkdir: %+v", event)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("the directory was not made: %v", err)
	}
}

// A read of a path the sandbox cannot read is an error, not the same answer as a missing file:
// the tool should say permission denied rather than "there is no such file".
func TestAnUnreadablePathIsAnError(t *testing.T) {
	_, socket := startServer(t)
	c := dial(t, socket)
	dir := t.TempDir()
	path := filepath.Join(dir, "private")
	if err := os.WriteFile(path, []byte("secret"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if os.Geteuid() == 0 {
		t.Skip("root can read a mode-000 file; the error path is not reachable as root")
	}
	event := fileAnswer(t, c, Request{Type: "read", ID: "r", Path: path})
	if event.Type != "error" || !strings.Contains(strings.ToLower(event.Error), "permission") {
		t.Fatalf("read: %+v", event)
	}
}
