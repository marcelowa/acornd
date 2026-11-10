package control

import (
	"slices"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/sandbox"
)

// A worker whose sandbox could not be prepared is still told its name and engine, so its first
// tool call can ask control to try again. Starting it with no name at all was the state that
// left the workspace dead until the gateway restarted.
func TestAWorkerIsToldTheSandboxItMustWake(t *testing.T) {
	t.Setenv(sandbox.TestHostEnv, "1")
	path := t.TempDir()
	tier := sandbox.NewTier(t.TempDir(), "", nil)
	control := New(Options{
		Workspace: Workspace{Name: "w", Path: path, Engine: "host"},
		DataDir:   t.TempDir(),
		Sandboxes: tier,
	})
	args := control.sandboxArgs(SandboxState{Tools: false, Problem: "the cgroup was busy"})
	for _, want := range []string{
		"--sandbox", sandbox.Name(path),
		"--sandbox-engine", "host",
		"--sandbox-problem", "the cgroup was busy",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("args should carry %q: %v", want, args)
		}
	}
}

// With no engine at all there is nothing to wake; the reason still travels, so the refusal the
// worker shows is the machine's sentence rather than "no sandbox is configured".
func TestAWorkerWithNoEngineIsToldWhy(t *testing.T) {
	control := New(Options{
		Workspace: Workspace{Name: "w", Path: t.TempDir()},
		DataDir:   t.TempDir(),
	})
	args := control.sandboxArgs(SandboxState{Tools: false, Problem: "no sandbox engine can run on this machine"})
	if !slices.Contains(args, "--sandbox-problem") || !slices.Contains(args, "no sandbox engine can run on this machine") {
		t.Fatalf("args should carry the refusal: %v", args)
	}
	if slices.Contains(args, "--sandbox") {
		t.Fatalf("a worker with no engine must not be handed a name it cannot open: %v", args)
	}
}
