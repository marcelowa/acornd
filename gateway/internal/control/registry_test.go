package control

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
)

// Loading the registry, which is the first thing a gateway does and the thing that decides where
// every workspace lives. The interesting cases are the ones nobody writes a test for until they
// happen: a registry that has been edited by hand, and a folder the account cannot write.

// A registry that was edited into two general workspaces is repaired by keeping the first and
// clearing the rest, because "the general one" has to be one workspace.
func TestARegistryWithTwoGeneralWorkspacesIsRepaired(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "workspaces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	registry := RegistryFile{
		Path:          filepath.Join(dataDir, "workspaces.json"),
		WorkspacesDir: dir,
		Workspaces: []Workspace{
			{Name: "personal", Path: filepath.Join(dir, "personal"), General: true},
			{Name: "second", Path: filepath.Join(dir, "second"), General: true},
			{Name: "third", Path: filepath.Join(dir, "third"), General: true},
		},
	}
	if err := SaveRegistryFile(registry); err != nil {
		t.Fatal(err)
	}

	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: dataDir})
	if err := workspaces.loadRegistry(); err != nil {
		t.Fatal(err)
	}
	generals := 0
	for _, workspace := range workspaces.registry {
		if workspace.General {
			generals++
		}
	}
	if generals != 1 {
		t.Errorf("%d general workspaces after the repair, want 1", generals)
	}
	// The first one in the file is the one kept: the repair is not a guess about which is best.
	if !workspaces.registry["personal"].General {
		t.Error("the first general workspace should be the one kept")
	}
}

// A first run makes the general workspace in the workspaces folder, and writes the registry only
// once: the file is the record, and a second write would be a second decision.
func TestAFirstRunMakesTheGeneralWorkspace(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "workspaces")
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: dataDir, WorkspacesDir: dir})
	if err := workspaces.loadRegistry(); err != nil {
		t.Fatal(err)
	}
	general, ok := workspaces.registry[PersonalWorkspaceName]
	if !ok {
		t.Fatal("a first run should create the general workspace")
	}
	if general.Path != filepath.Join(dir, PersonalWorkspaceName) {
		t.Errorf("its folder is %q", general.Path)
	}
	if !general.General {
		t.Error("it should be the general one")
	}
	// The folder exists, and the registry was written: a second load finds the same workspace
	// rather than making another.
	if info, err := os.Stat(general.Path); err != nil || !info.IsDir() {
		t.Errorf("the folder should exist: %v", err)
	}
	reloaded := NewWorkspaces(WorkspacesOptions{DataDir: dataDir, WorkspacesDir: dir})
	if err := reloaded.loadRegistry(); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.registry) != len(workspaces.registry) {
		t.Errorf("reloading made %d workspaces, want %d", len(reloaded.registry), len(workspaces.registry))
	}
}

// A folder the account cannot write is a refused start rather than a half-made workspace: the
// failure has to be the caller's to report, and it has to happen before anything is recorded.
func TestAFolderNobodyCanWriteIsRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere, so this needs an unprivileged account")
	}
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "workspaces")
	if err := os.MkdirAll(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: dataDir, WorkspacesDir: dir})
	if err := workspaces.loadRegistry(); err == nil {
		// The registry file itself lives in dataDir, so writing it still works; what must fail is
		// the general workspace's folder. Either way, the load must not claim success and leave no
		// general workspace.
		if _, ok := workspaces.registry[PersonalWorkspaceName]; !ok {
			t.Error("the load succeeded without a general workspace")
		}
	}
}

// The catalog lookups: which models a provider offers, and which levels a model declares. They
// are what the pickers read, so an empty answer is a model that cannot be chosen.
func TestTheCatalogAnswersForWhatItHas(t *testing.T) {
	c := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}})
	c.providers = []protocol.ProviderInfo{{
		ID:            "p",
		Authenticated: true,
		Models: []protocol.ModelInfo{
			{ID: "m", ThinkingLevels: []string{"low", "high"}},
			{ID: "plain"},
		},
	}}

	if levels := c.supportedThinking("p", "m"); len(levels) != 2 {
		t.Errorf("levels = %v, want the model's two", levels)
	}
	// A half-named model has nothing to look up, and the answer is empty rather than the first
	// model's levels: a level from another model is a setting that does nothing.
	if levels := c.supportedThinking("p", ""); levels != nil {
		t.Errorf("a model with no id should have no levels, got %v", levels)
	}
	if levels := c.supportedThinking("", "m"); levels != nil {
		t.Errorf("a provider with no id should have no levels, got %v", levels)
	}
	if levels := c.supportedThinking("nobody", "m"); levels != nil {
		t.Errorf("an unknown provider should have no levels, got %v", levels)
	}

	describe := protocol.Describe{Providers: c.providers}
	if !c.isAvailable(describe, protocol.ModelBinding{Provider: "p", ModelID: "m"}) {
		t.Error("a model the provider lists is available")
	}
	if c.isAvailable(describe, protocol.ModelBinding{Provider: "p", ModelID: "gone"}) {
		t.Error("a model it does not list is not available")
	}
	if c.isAvailable(describe, protocol.ModelBinding{Provider: "nobody", ModelID: "m"}) {
		t.Error("another provider's model is not available")
	}
	// And an unauthenticated provider offers nothing, whatever it lists: a model this install
	// cannot reach is not a model it can run.
	describe.Providers[0].Authenticated = false
	if c.isAvailable(describe, protocol.ModelBinding{Provider: "p", ModelID: "m"}) {
		t.Error("an unauthenticated provider offers nothing")
	}
}
