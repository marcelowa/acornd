package control

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/sandbox"
	"github.com/marcelowa/acornd/gateway/internal/vault"
)

// What reaches a sandbox is decided here, and the two engines decide differently: a substitute
// engine keeps the value on the host and needs to know which hosts it may go to, and an
// environment engine puts it in the process and needs the admin to have said that is allowed. A
// secret that fails either rule stays visible with its reason, because a list that silently drops
// one is a list nobody can debug.

func vaultWith(t *testing.T, entries ...vault.Upsert) *vault.Store {
	t.Helper()
	store, err := vault.Open(filepath.Join(t.TempDir(), "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if _, err := store.Upsert(entry); err != nil {
			t.Fatalf("%s: %v", entry.Name, err)
		}
	}
	return store
}

func controlWithVault(t *testing.T, store *vault.Store) *Control {
	t.Helper()
	return New(Options{
		Workspace:   Workspace{Name: "w", Path: t.TempDir()},
		DataDir:     t.TempDir(),
		StoragePath: t.TempDir() + "/storage",
		Vault:       store,
	})
}

func TestASubstituteEngineNeedsAHostForEverySecret(t *testing.T) {
	withHosts := []string{"api.example.com"}
	store := vaultWith(t,
		vault.Upsert{Name: "REACHABLE", Scope: vault.Global, Description: "d", Value: "v", Hosts: &withHosts},
		vault.Upsert{Name: "NOWHERE", Scope: vault.Global, Description: "d", Value: "v"},
	)
	control := controlWithVault(t, store)
	resolved := control.resolveSecrets(sandbox.DeliverySubstitute)

	if len(resolved.delivered) != 1 || resolved.delivered[0].Name != "REACHABLE" {
		t.Errorf("only the secret with a host is delivered: %v", resolved.delivered)
	}
	if len(resolved.state) != 2 {
		t.Fatalf("both stay visible in the state: %v", resolved.state)
	}
	for _, state := range resolved.state {
		if state.Name == "NOWHERE" && state.Status != "not delivered: no allowed hosts" {
			t.Errorf("the reason should say why: %+v", state)
		}
		if state.Name == "REACHABLE" && state.Status != "delivered" {
			t.Errorf("a delivered secret says so: %+v", state)
		}
		// The placeholder is what the prompt sees: never the value, and only where the engine
		// substitutes.
		if state.Name == "REACHABLE" && state.Placeholder != "$MSB_REACHABLE" {
			t.Errorf("placeholder = %q", state.Placeholder)
		}
	}
}

func TestAnEnvironmentEngineNeedsTheAdminToAllowIt(t *testing.T) {
	// AllowEnv defaults to true — the card asks, and a value somebody typed into a form is a value
	// they meant to give — so the refusal is the opt-out, not the default.
	allowed := true
	forbidden := false
	store := vaultWith(t,
		vault.Upsert{Name: "ALLOWED", Scope: vault.Global, Description: "d", Value: "v", AllowEnv: &allowed},
		vault.Upsert{Name: "REFUSED", Scope: vault.Global, Description: "d", Value: "v", AllowEnv: &forbidden},
	)
	control := controlWithVault(t, store)
	resolved := control.resolveSecrets(sandbox.DeliveryEnvironment)

	if len(resolved.delivered) != 1 || resolved.delivered[0].Name != "ALLOWED" {
		t.Errorf("only the entry the admin allowed is delivered: %v", resolved.delivered)
	}
	for _, state := range resolved.state {
		if state.Name == "REFUSED" && state.Status != "not delivered: its value may not enter the sandbox" {
			t.Errorf("the reason should say why: %+v", state)
		}
		// An environment engine has no placeholder: the value is simply in the process.
		if state.Placeholder != "" {
			t.Errorf("an environment engine has no placeholder, got %q", state.Placeholder)
		}
	}
}

// With no vault there is nothing to resolve, and with a vault that cannot be read the reason is
// carried rather than swallowed: a sandbox whose secrets vanished is a workspace whose tools fail
// for a reason nobody can see.
func TestAnUnreadableVaultIsAProblemRatherThanAnEmptyList(t *testing.T) {
	control := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}, DataDir: t.TempDir(), StoragePath: t.TempDir()})
	if resolved := control.resolveSecrets(sandbox.DeliverySubstitute); len(resolved.state) != 0 || resolved.problem != "" {
		t.Errorf("no vault, nothing to resolve: %+v", resolved)
	}

	// A secrets file this build cannot read: the shape of a hand-edit or a half-written save, and
	// the reason a workspace's tools would fail with no explanation.
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken, err := vault.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	control = controlWithVault(t, broken)
	unreadable := control.resolveSecrets(sandbox.DeliverySubstitute)
	if unreadable.problem == "" {
		t.Error("a vault that cannot resolve should carry the reason")
	}
}
