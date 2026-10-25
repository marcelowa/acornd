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

	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: dir})
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
	const dataDir, userHome = "/home/me/.acornfall", "/home/me"
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
			got := workspace.SandboxPolicy(dataDir, userHome).DisableVMNesting
			if got != test.want {
				t.Errorf("DisableVMNesting = %t, want %t", got, test.want)
			}
		})
	}
}

// Devices are on by default so a bwrap workspace can run another acornfall, and a workspace
// that does not want them can say so once.
func TestDevicesAreOnByDefaultAndTheWorkspaceCanWithdrawThem(t *testing.T) {
	const dataDir, userHome = "/home/me/.acornfall", "/home/me"
	on := true
	if !(Workspace{}).SandboxPolicy(dataDir, userHome).Devices {
		t.Fatal("a workspace that asked for nothing should get the devices")
	}
	if (Workspace{DisableDevices: &on}).SandboxPolicy(dataDir, userHome).Devices {
		t.Fatal("a workspace that withdrew the devices should not get them")
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
		DataDir:   t.TempDir(),
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

// Where a workspace is has to be answerable while it is opening: the interface asks this on a
// timer, and a workspace's start is the request that takes seconds.
func TestProgressTracksAnOpeningWorkspace(t *testing.T) {
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: t.TempDir()})
	if got := workspaces.Progress("slow"); got.Status != StatusStopped {
		t.Fatalf("a workspace nobody asked for is stopped, got %q", got.Status)
	}
	// The stages come from the code doing the work, through the callback the layer hands it.
	stage := workspaces.stage("slow")
	stage("preparing the image")
	opening := workspaces.Progress("slow")
	if opening.Status != StatusOpening || opening.Stage != "preparing the image" {
		t.Fatalf("the step should be reported as it happens, got %+v", opening)
	}
	workspaces.setProgress("slow", Progress{Status: StatusReady})
	if got := workspaces.Progress("slow"); got.Status != StatusReady || got.Stage != "" {
		t.Fatalf("a ready workspace says ready and nothing else, got %+v", got)
	}
	// The list carries the same answer, because that is what the rail reads.
	views := Views([]Workspace{{Name: "slow"}}, workspaces.Progress)
	if len(views) != 1 || views[0].Status != StatusReady {
		t.Fatalf("the view should carry the status, got %+v", views)
	}
}

// The engine list is a machine answer that moves while the gateway runs: the microVM engine
// downloads itself on a workspace's first use. A cached "not installed" that outlives the
// download leaves the chooser refusing the engine this machine now has.
func TestTheEngineListNoticesAnEngineInstalledWhileRunning(t *testing.T) {
	dir := t.TempDir()
	workspaces := NewWorkspaces(WorkspacesOptions{
		DataDir:   dir,
		Sandboxes: sandbox.NewTier(dir, "", nil),
	})
	installed := func() bool {
		for _, engine := range workspaces.Engines() {
			if engine.Name == "microsandbox" {
				return engine.Installed
			}
		}
		t.Fatal("the microVM engine is missing from the list")
		return false
	}

	if installed() {
		t.Fatal("nothing installs the engine before a workspace first asks for it")
	}
	// First use puts the pinned binary where the driver looks for it.
	binary := filepath.Join(dir, "tools", "microsandbox", sandbox.Version, "bin", "msb")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatalf("engine folder: %v", err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("engine binary: %v", err)
	}
	if installed() {
		t.Fatal("the answer is kept until a workspace start drops it, not probed per request")
	}

	workspaces.refreshEngines()

	if !installed() {
		t.Fatal("after a workspace start the chooser should see the engine that landed")
	}
}

// The engine list carries the domain's answer to "does this engine substitute?", because the
// interface's copy and its host requirement follow it. Without it the interface would keep its own
// list of engine names, which is wrong the day a third engine arrives.
func TestTheEngineListSaysWhichEnginesSubstitute(t *testing.T) {
	t.Parallel()
	tier := sandbox.NewTier(t.TempDir(), "", nil)
	manager, err := tier.For("bwrap")
	if err != nil {
		t.Fatal(err)
	}
	workspaces := NewWorkspaces(WorkspacesOptions{WorkspacesDir: t.TempDir(), Sandbox: manager, Sandboxes: tier})
	info := workspaces.engineInfo("bwrap", false)
	if !info.Substitutes {
		t.Error("bwrap substitutes: the value stays on the host and a placeholder goes in")
	}
}
