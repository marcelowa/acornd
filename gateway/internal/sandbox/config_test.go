package sandbox

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/supervise"
)

// The bubblewrap engine writes one config per workspace and reads it back when the sandbox starts,
// so what it refuses when writing is what a workspace can be. Everything here is checked before
// anything is created, because a half-created sandbox is a folder somebody has to find and delete.

func TestAPolicyNeedsAWorkspaceThatExists(t *testing.T) {
	engine := NewBwrap(t.TempDir())
	for _, one := range []struct {
		why    string
		policy Policy
		says   string
	}{
		{"no workspace named", Policy{}, "no workspace"},
		{"a folder that is not there", Policy{Workspace: filepath.Join(t.TempDir(), "missing")}, "no workspace folder"},
		{"a file where the folder should be", Policy{Workspace: writeAFile(t)}, "no workspace folder"},
	} {
		if err := engine.validatePolicy(one.policy); err == nil {
			t.Errorf("%s: should be refused", one.why)
		} else if !strings.Contains(err.Error(), one.says) {
			t.Errorf("%s: answered %v", one.why, err)
		}
	}

	// And a real folder passes.
	if err := engine.validatePolicy(Policy{Workspace: t.TempDir()}); err != nil {
		t.Errorf("a folder that exists should be accepted: %v", err)
	}
}

func writeAFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(path, []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The state folder is the engine's own, and it has to be private: 0700, made once, and reported
// with the reason when it cannot be made — a world-readable folder here is every sandbox's
// filesystem visible to every user.
func TestTheStateFoldersAreMadePrivateAndReportedWhenTheyCannotBe(t *testing.T) {
	dataDir := t.TempDir()
	engine := NewBwrap(dataDir)
	home, err := engine.stateDirs(Policy{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		t.Fatalf("the workspace's state folder: %v", err)
	}
	if info, err := os.Stat(engine.state); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the engine's state folder should be 0700: %v %v", info, err)
	}

	// A file in the way of the state folder is reported rather than ignored.
	blocked := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(blocked, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := NewBwrap(t.TempDir()).WithStateDir(blocked)
	if _, err := broken.stateDirs(Policy{Workspace: t.TempDir()}); err == nil {
		t.Fatal("a state folder under a file should be reported")
	}
}

// Starting a sandbox whose config was never written says so: there is nothing to boot, and the
// refusal names the workspace rather than failing somewhere inside a supervisor.
func TestStartingASandboxWithNoConfigIsRefused(t *testing.T) {
	engine := NewBwrap(t.TempDir())
	if err := engine.Start(context.Background(), "never-created", nil); err == nil {
		t.Fatal("a sandbox with no config should not start")
	} else if !strings.Contains(err.Error(), "never-created") {
		t.Errorf("the refusal should name the workspace: %v", err)
	}
}

// A sandbox that is already answering is started by returning: starting it again would be a second
// supervisor on the same socket, and the one already running owns the workspace's processes.
func TestStartingASandboxThatAnswersIsANoOp(t *testing.T) {
	engine := NewBwrap(t.TempDir())
	name := "answering"
	dir := engine.workspaceDir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A config that names a socket this test is listening on: the engine dials it, hears an answer,
	// and stops before booting anything.
	socket := answerPings(t)
	config := bwrapConfig{Socket: socket, Network: bwrapNetwork{Mode: "off"}}
	payload, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	// The config lives in the engine's state folder, named for the workspace.
	if err := os.MkdirAll(engine.state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(engine.configPath(name), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(context.Background(), name, nil); err != nil {
		t.Fatalf("a sandbox that answers should be left alone: %v", err)
	}
}

// The globals a policy names are mounted read-only only where they exist: a path this machine does
// not have is a mount that would fail, and the list is sorted so the argv is the same everywhere.
func TestOnlyGlobalsThatExistAreMounted(t *testing.T) {
	present := t.TempDir()
	missing := filepath.Join(t.TempDir(), "not-here")
	globals := readonlyGlobals(Policy{Global: []string{missing, present}})
	if len(globals) != 1 || globals[0] != present {
		t.Errorf("globals = %v, want only the one that exists", globals)
	}
	later := t.TempDir()
	sorted := readonlyGlobals(Policy{Global: []string{present, later}})
	if len(sorted) != 2 || sorted[0] > sorted[1] {
		t.Errorf("globals should be sorted: %v", sorted)
	}
}

// A resolver file the sandbox was given is used; without one it reads the machine's, because a
// sandbox with no DNS resolves nothing.
func TestTheResolverIsTheOneTheConfigNamesOrTheMachines(t *testing.T) {
	if got := resolverOrHost(bwrapConfig{ResolvConf: "/run/acornfall/resolv.conf"}); got != "/run/acornfall/resolv.conf" {
		t.Errorf("resolver = %q", got)
	}
	if got := resolverOrHost(bwrapConfig{}); got != "/etc/resolv.conf" {
		t.Errorf("without one the machine's is used, got %q", got)
	}
}

// answerPings is a supervisor that is up: it decodes one request and answers "ready", which is what
// the engine's dial asks for. It is the cheapest way to be a sandbox that is already running.
func answerPings(t *testing.T) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "s.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				var request supervise.Request
				_ = json.NewDecoder(connection).Decode(&request)
				_ = json.NewEncoder(connection).Encode(supervise.Event{Type: "ready"})
			}()
		}
	}()
	return socket
}
