package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The install folder holds credentials.json, token, and state/, which an agent must never
// reach. Everything it may read lives in a named resource folder, which is why the mount
// list is spelled out rather than mounting the install and hoping.
func TestGlobalPathsAreTheResourceFoldersAndNeverTheInstall(t *testing.T) {
	dataDir := t.TempDir()
	userHome := t.TempDir()
	if err := EnsureGlobal(dataDir, userHome); err != nil {
		t.Fatal(err)
	}
	for _, folder := range globalFolders(dataDir, userHome) {
		info, err := os.Stat(folder)
		if err != nil || !info.IsDir() {
			t.Errorf("%s should exist after EnsureGlobal: %v", folder, err)
		}
	}

	paths := GlobalPaths(dataDir, userHome)
	want := []string{
		filepath.Join(userHome, ".agents"),
		filepath.Join(dataDir, "agents"),
		filepath.Join(dataDir, "skills"),
		filepath.Join(dataDir, "prompts"),
		filepath.Join(dataDir, "commands"),
	}
	if !slices.Equal(paths, want) {
		t.Errorf("the mounted paths should be the resource folders, got %v want %v", paths, want)
	}
	for _, forbidden := range []string{dataDir, filepath.Join(dataDir, "credentials.json"), filepath.Join(dataDir, "state")} {
		if slices.Contains(paths, forbidden) {
			t.Errorf("%s must never be mounted into a sandbox", forbidden)
		}
	}
}

// The engine refuses a mount of a path that is not there, and a file cannot be created on
// the user's behalf the way an empty folder can: config.json is mounted when it exists, and
// a sandbox made before it was written does not see it until it is recreated.
func TestTheGlobalConfigIsMountedOnlyWhenItExists(t *testing.T) {
	dataDir := t.TempDir()
	userHome := t.TempDir()
	if err := EnsureGlobal(dataDir, userHome); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dataDir, "config.json")
	if slices.Contains(GlobalPaths(dataDir, userHome), config) {
		t.Error("an install with no config.json should not mount one")
	}
	if err := os.WriteFile(config, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(GlobalPaths(dataDir, userHome), config) {
		t.Error("config.json should be mounted once it exists")
	}
}

func TestNameIsStableAndReadable(t *testing.T) {
	name := Name("/home/marcelo/workspace/acornfall")
	if !strings.HasPrefix(name, "acornfall-acornfall-") {
		t.Errorf("name should name the folder, got %q", name)
	}
	if name != Name("/home/marcelo/workspace/acornfall") {
		t.Error("the same workspace must produce the same name; the engine's state is keyed by it")
	}
	if Name("/a/acornfall") == Name("/b/acornfall") {
		t.Error("two folders with the same base name must not share a sandbox")
	}
}

func TestNameSurvivesOddFolders(t *testing.T) {
	for _, path := range []string{"/", "", "///", "/a/../b", "/tmp/weird name!"} {
		name := Name(path)
		if name == "" || strings.ContainsAny(name, " /!") {
			t.Errorf("Name(%q) = %q, which the engine would refuse", path, name)
		}
	}
}

// A workspace that runs something tells the engine not to stop it for being idle. Control owns
// the rule, and this is how the policy says so to an engine that keeps its own timer.
func TestAKeptAliveWorkspaceIsNotStoppedForBeingIdle(t *testing.T) {
	kept := Policy{Image: DefaultImage, Workspace: "/work/ws"}
	if got := kept.withDefaults().IdleTimeout; got != DefaultIdleTimeout {
		t.Fatalf("default idle timeout: %q", got)
	}
	kept.IdleTimeout = KeptAliveIdleTimeout
	if kept.withDefaults().IdleTimeout != KeptAliveIdleTimeout {
		t.Fatal("a policy's own idle timeout must survive the defaults")
	}
	if KeptAliveIdleTimeout == DefaultIdleTimeout {
		t.Fatal("a kept-alive workspace must ask for a different timeout than the default")
	}
}
