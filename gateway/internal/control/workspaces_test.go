package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/sandbox"
)

// Where workspaces live is a decision someone makes, not one to guess.
//
// Without a home directory the old code fell back to a relative "acornfall/workspaces", which
// put workspaces wherever the process happened to start — a folder nobody chose, appearing in a
// container's working directory. An error names the flag that settles it instead.
func TestNoHomeDirectoryIsAnErrorNotAGuess(t *testing.T) {
	t.Setenv("HOME", "")

	dir, err := DefaultWorkspacesDir()
	if err == nil {
		t.Fatalf("expected an error with no home directory, got %q", dir)
	}
	if !strings.Contains(err.Error(), "--workspaces-dir") {
		t.Errorf("the error should name the flag that fixes it, got %q", err)
	}
	if strings.Contains(dir, "acornfall") {
		t.Errorf("no path should be invented, got %q", dir)
	}
}

// A key this build no longer writes is dropped the next time the registry is read: a stale
// value that nothing reads is a value something can read again by accident.
func TestARegistryFromAnOlderBuildIsRewrittenWithoutOldKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workspaces.json")
	legacy := `{
  "workspacesDir": "` + dir + `/workspaces",
  "workspaces": [
    {"name": "general", "path": "` + dir + `/general", "title": "general", "general": true, "trusted": true},
    {"name": "old", "path": "` + dir + `/old", "title": "old", "trusted": false}
  ]
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	workspaces := NewWorkspaces(WorkspacesOptions{Home: dir})
	if err := workspaces.loadRegistry(); err != nil {
		t.Fatal(err)
	}
	if len(workspaces.List()) != 2 {
		t.Fatalf("both workspaces should load, got %d", len(workspaces.List()))
	}

	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "trusted") {
		t.Errorf("the registry should have been rewritten without the dropped key:\n%s", payload)
	}
	// And what it did say is intact.
	reloaded, err := LoadRegistryFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Workspaces) != 2 || reloaded.Workspaces[1].Path != filepath.Join(dir, "old") {
		t.Errorf("the rewrite lost something: %+v", reloaded.Workspaces)
	}
}

// Nesting is allowed unless this workspace refuses it: the install has no say any more, and an
// unset field means "nest when the machine and the engine can", which is not the same as false.
func TestNestingIsTheWorkspacesOwnDecision(t *testing.T) {
	const home, userHome = "/home/me/.acornfall", "/home/me"
	on, off := true, false
	cases := []struct {
		name      string
		workspace *bool
		want      bool
	}{
		{"unset nests whenever it can", nil, false},
		{"a workspace can refuse", &on, true},
		{"a workspace can allow", &off, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			workspace := Workspace{DisableVMNesting: test.workspace}
			got := workspace.SandboxPolicy(home, userHome).DisableVMNesting
			if got != test.want {
				t.Errorf("DisableVMNesting = %t, want %t", got, test.want)
			}
		})
	}
}

// A workspace's sandbox belongs to the engine the workspace chose. Removing it must ask that
// engine: the default one's manager answers "no such sandbox" and the disk stays — which is what
// a real install showed, 88 MB per forgotten bwrap workspace.
func TestAWorkspacesSandboxIsRemovedByItsOwnEngine(t *testing.T) {
	tier := sandbox.NewTier(t.TempDir(), "", nil)
	bwrap, err := tier.For("bwrap")
	if err != nil {
		t.Fatalf("tier: %v", err)
	}
	other, err := tier.For("host")
	if err != nil {
		t.Fatalf("tier: %v", err)
	}
	workspaces := NewWorkspaces(WorkspacesOptions{
		Home:      t.TempDir(),
		Sandbox:   other, // the install's default is something else entirely
		Sandboxes: tier,
	})

	if got := workspaces.sandboxManagerFor(Workspace{Name: "w", Engine: "bwrap"}); got != bwrap {
		t.Fatalf("a bwrap workspace resolves to %q, want the bwrap manager", got.Driver().Name())
	}
	// A workspace that named no engine still gets the install's default.
	if got := workspaces.sandboxManagerFor(Workspace{Name: "w"}); got != other {
		t.Fatalf("a default workspace resolves to %q, want the install default", got.Driver().Name())
	}
}

// The grant is a workspace's own field and reaches the engine's policy as one.
func TestAPrivateNetworksGrantReachesThePolicy(t *testing.T) {
	workspace := Workspace{Name: "w", Path: "/work/w"}
	if workspace.SandboxPolicy("/home", "/users/u").PrivateEgress {
		t.Fatal("a workspace that asked for nothing should not reach private networks")
	}
	workspace.Private = true
	if !workspace.SandboxPolicy("/home", "/users/u").PrivateEgress {
		t.Fatal("a workspace that asked for private networks should carry the grant")
	}
}
