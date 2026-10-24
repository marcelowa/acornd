package control

import (
	"context"
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/sandbox"
)

// A sandbox that was here before a secret changed has to be brought up to date, and no worker is
// attached yet — which is exactly the safe moment to restart it: the first turn should not begin
// with an environment that predates the vault.
//
// The host engine is the one that can be driven end to end here: its delivery is the environment,
// it always answers "restart required", and stopping and producing it touch nothing.

func hostControl(t *testing.T) (*Control, *sandbox.Manager) {
	t.Helper()
	tier := sandbox.NewTier(t.TempDir(), "", nil)
	manager, err := tier.For("host")
	if err != nil {
		t.Fatal(err)
	}
	control := New(Options{
		Workspace:   Workspace{Name: "w", Path: t.TempDir(), Engine: "host"},
		DataDir:     t.TempDir(),
		StoragePath: t.TempDir() + "/storage",
		Sandboxes:   tier,
	})
	return control, manager
}

func TestAStoredSecretRestartsTheSandboxBeforeTheFirstTurn(t *testing.T) {
	control, manager := hostControl(t)
	policy := control.opts.Workspace.SandboxPolicy(control.opts.DataDir, control.opts.UserHome)
	state := SandboxState{}

	control.reconcileStoredSecrets(context.Background(), manager, policy, sandboxSecrets{}, &state)

	// The engine said a restart is required, the stop and the produce both succeeded, so nothing is
	// left owed to the next turn.
	if state.SecretsProblem != "" {
		t.Errorf("the change should have landed: %q", state.SecretsProblem)
	}
	if state.SecretsRestart {
		t.Error("a restart that happened is not still owed")
	}
}

// A sandbox that has to restart and cannot be brought back leaves the restart owed: the next turn
// would otherwise run with an environment that predates the vault. The image reference is invalid
// on purpose, so the produce fails at the parse rather than at a registry — a unit test that
// reached docker.io would be a test that needs the network and a rate limit.
func TestASandboxThatCannotBeReproducedLeavesTheRestartOwed(t *testing.T) {
	tier := sandbox.NewTier(t.TempDir(), "", nil)
	manager, err := tier.For("bwrap")
	if err != nil {
		t.Fatal(err)
	}
	control := New(Options{
		Workspace:   Workspace{Name: "w", Path: t.TempDir(), Engine: "bwrap"},
		DataDir:     t.TempDir(),
		StoragePath: t.TempDir() + "/storage",
		Sandboxes:   tier,
	})
	policy := control.opts.Workspace.SandboxPolicy(control.opts.DataDir, control.opts.UserHome)
	policy.Image = "::not a reference::"
	state := SandboxState{}

	control.reconcileStoredSecrets(context.Background(), manager, policy, sandboxSecrets{}, &state)

	if !state.SecretsRestart {
		t.Fatal("a sandbox that could not be brought back owes the restart to the next turn")
	}
}

// Provisioning reports both halves: what the vault resolved for the screen, and whether the
// sandbox is usable. A workspace with no engine still says what its secrets are, so the screen can
// show them beside the refusal instead of an empty list.
func TestProvisioningCarriesTheResolvedSecretsBesideTheState(t *testing.T) {
	control, _ := hostControl(t)
	state := control.provisionSandbox(context.Background())
	if state.Problem != "" {
		t.Fatalf("the host engine should be usable: %q", state.Problem)
	}
	if !state.Tools {
		t.Error("a workspace with a working engine runs tools")
	}
	if state.Engine == "" {
		t.Errorf("the state should name the engine: %+v", state)
	}
	// The secrets the vault resolved travel with the state, so the screen can show them beside the
	// refusal or the engine rather than an empty list.
	if state.Secrets == nil && state.SecretsProblem != "" {
		t.Errorf("a refusal should still carry what was resolved: %+v", state)
	}
}
