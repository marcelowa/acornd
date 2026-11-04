package control

import (
	"errors"
	"strings"
	"testing"
)

// A machine that can run no sandbox says why in the workspace's state. "no sandbox is
// configured" reads like a choice somebody made; this one is the machine, and the sentence a
// person reads should be the same one doctor prints.
func TestTheStateSaysWhyThereIsNoSandbox(t *testing.T) {
	t.Parallel()
	control := controlWithWorker(t, &fakeWorker{})
	control.opts.SandboxProblem = errors.New("no sandbox engine can run on this machine: bwrap: no user namespace")
	state := control.sandboxRefusal()
	if !strings.Contains(state.Problem, "no user namespace") {
		t.Errorf("the state should carry what the machine is missing: %q", state.Problem)
	}
	if !strings.Contains(state.Problem, "refuse") {
		t.Errorf("the state should say what the tools do about it: %q", state.Problem)
	}
	if state.Tools {
		t.Error("no sandbox means no tools")
	}
}

// A run that was told to go without a sandbox is a different answer: nothing is missing, so
// there is nothing to explain.
func TestARunToldToGoWithoutASandboxSaysThat(t *testing.T) {
	t.Parallel()
	control := controlWithWorker(t, &fakeWorker{})
	if got := control.sandboxRefusal().Problem; !strings.Contains(got, "tool execution is disabled") {
		t.Fatalf("an install asked to run without a sandbox: %q", got)
	}
}
