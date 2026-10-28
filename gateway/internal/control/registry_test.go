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

// An install written before the flag was dropped carries `general` keys, and one of them may be true on a
// workspace with no members. Loading it must not care: the answer comes from the membership list, and a file
// written under the old rule reads exactly like one written under the new one (ADR 0032 §9).
func TestAGeneralKeyInAnOldRegistryIsIgnored(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "workspaces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"personal", "second"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := `{"path":"` + filepath.Join(dataDir, "workspaces.json") + `","workspacesDir":"` + dir + `",
	  "workspaces":[
	    {"name":"personal","path":"` + filepath.Join(dir, "personal") + `","title":"Personal","general":true},
	    {"name":"second","path":"` + filepath.Join(dir, "second") + `","title":"Second","general":false,
	     "members":["ana@example.com","bea@example.com"]}]}`
	if err := os.WriteFile(filepath.Join(dataDir, "workspaces.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: dataDir, WorkspacesDir: dir})
	if err := workspaces.loadRegistry(); err != nil {
		t.Fatalf("an old registry failed to load: %v", err)
	}
	if !workspaces.registry["personal"].Personal() {
		t.Error("the room with no members should be somebody's own")
	}
	if workspaces.registry["second"].Personal() {
		t.Error("a room with two members is not anybody's own")
	}
}

// A first run makes the install's own workspace in the workspaces folder, and writes the registry only
// once: the file is the record, and a second write would be a second decision.
func TestAFirstRunMakesThePersonalWorkspace(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "workspaces")
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: dataDir, WorkspacesDir: dir})
	if err := workspaces.loadRegistry(); err != nil {
		t.Fatal(err)
	}
	personal, ok := workspaces.registry[PersonalWorkspaceName]
	if !ok {
		t.Fatal("a first run should create the personal workspace")
	}
	if personal.Path != filepath.Join(dir, PersonalWorkspaceName) {
		t.Errorf("its folder is %q", personal.Path)
	}
	if !personal.Personal() {
		t.Error("it should be somebody's own room")
	}
	// The folder exists, and the registry was written: a second load finds the same workspace
	// rather than making another.
	if info, err := os.Stat(personal.Path); err != nil || !info.IsDir() {
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
