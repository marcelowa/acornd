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

// Nesting is allowed unless refused, and whose refusal it is matters: the install's flag
// is the default, a workspace's own setting wins over it, and nil is not false — it is
// "follow the install", which is exactly why the field is a pointer.
func TestNestingFollowsTheInstallUnlessTheWorkspaceChooses(t *testing.T) {
	const home, userHome = "/home/me/.acornfall", "/home/me"
	on, off := true, false
	cases := []struct {
		name      string
		workspace *bool
		install   bool
		want      bool
	}{
		{"no setting follows an install that allows", nil, false, false},
		{"no setting follows an install that refuses", nil, true, true},
		{"a workspace can refuse what the install allows", &on, false, true},
		{"a workspace can allow what the install refuses", &off, true, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			workspace := Workspace{DisableVMNesting: test.workspace}
			got := workspace.SandboxPolicy(home, userHome, test.install).DisableVMNesting
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
	if workspace.SandboxPolicy("/home", "/users/u", false).PrivateEgress {
		t.Fatal("a workspace that asked for nothing should not reach private networks")
	}
	workspace.Private = true
	if !workspace.SandboxPolicy("/home", "/users/u", false).PrivateEgress {
		t.Fatal("a workspace that asked for private networks should carry the grant")
	}
}

// A workspace that named no engine is written down as the engine this machine runs, once, and a
// workspace that named one keeps it: the record is what the workspace is, not what was preferred
// on the machine that happened to start it.
func TestAnUnnamedEngineIsRecordedOnce(t *testing.T) {
	dir := t.TempDir()
	registry := filepath.Join(dir, "workspaces.json")
	seed := `{
  "workspacesDir": "` + dir + `/workspaces",
  "workspaces": [
    {"name": "general", "path": "` + dir + `/general", "title": "general", "general": true},
    {"name": "lab", "path": "` + dir + `/lab", "title": "lab", "engine": "host"}
  ]
}`
	if err := os.WriteFile(registry, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	// The machine's answer is a stand-in: this test is about recording, not probing.
	tier := sandbox.NewTier(dir, "", nil)
	machine, err := tier.For("bwrap")
	if err != nil {
		t.Fatalf("tier: %v", err)
	}
	workspaces := NewWorkspaces(WorkspacesOptions{Home: dir, Sandbox: machine, Sandboxes: tier})
	if err := workspaces.loadRegistry(); err != nil {
		t.Fatal(err)
	}
	recorded, err := workspaces.RecordDefaultEngines()
	if err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("one workspace named no engine, so one should be recorded, got %d", recorded)
	}
	reloaded, err := LoadRegistryFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	for _, workspace := range reloaded.Workspaces {
		switch workspace.Name {
		case "general":
			if workspace.Engine != "bwrap" {
				t.Errorf("general should have recorded bwrap, got %q", workspace.Engine)
			}
		case "lab":
			if workspace.Engine != "host" {
				t.Errorf("a workspace that named host keeps host, got %q", workspace.Engine)
			}
		}
	}
	// Running again changes nothing: the recording is once, not every start.
	if again, err := workspaces.RecordDefaultEngines(); err != nil || again != 0 {
		t.Fatalf("the second run recorded %d, %v: want nothing", again, err)
	}
}
