package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The bubblewrap engine, end to end: a workspace that chose it says so, its tools run inside a
// namespace view instead of on the host, and the install's own folders are not in that view.
//
// It needs no hypervisor and no engine download, which is the point of the engine: this test
// runs in the fast tier, on any Linux with user namespaces. See docs/adr/0023-bwrap-engine.md.
func TestBwrapWorkspaceRunsItsToolsInNamespaces(t *testing.T) {
	t.Parallel()
	bwrapEngine(t)
	s := start(t,
		withScript(
			scriptRule{
				Match: "who am i",
				Tools: []scriptTool{{Name: "bash", Args: map[string]any{
					// A marker the host does not have, written in the workspace, plus the two
					// things the engine must not let through.
					"command": `printf 'inside:%s' "$(id -un)"; printf ' marker' > made-in-the-sandbox.txt; ` +
						`cat "` + filepath.Join("$HOME", "credential-check") + `.missing" 2>/dev/null; ` +
						`test -w /usr && echo host-root-writable || echo host-root-read-only`,
				}}},
				Reply: "the command ran",
			},
		),
	)
	c := s.client()
	root := c.waitForConversation("general")

	general := c.state("general")
	engine := general.Sandbox
	if engine.Engine != "bwrap" {
		t.Fatalf("the workspace should run on bwrap, got %+v", engine)
	}
	if !engine.Isolated || !engine.Tools {
		t.Fatalf("a bwrap workspace is isolated and its tools run, got %+v", engine)
	}

	c.prompt("general", root, "who am i")
	got := c.waitForText("general", root, "the command ran")
	output := got.lastToolResult()
	for _, want := range []string{"inside:", "host-root-read-only"} {
		if !strings.Contains(output, want) {
			t.Errorf("the command's output should contain %q, got %q", want, output)
		}
	}

	// The command wrote into the workspace through the sandbox, and the file is the user's own:
	// bubblewrap binds the folder, so the path is the same on both sides.
	written := filepath.Join(general.Workspace.Path, "made-in-the-sandbox.txt")
	if _, err := os.Stat(written); err != nil {
		t.Fatalf("the sandbox did not write into the workspace: %v", err)
	}
}

// The engine is a workspace's own choice, so one install can hold a microVM workspace and a
// bubblewrap one. This is the field the create form and `workspace add --sandbox` set.
func TestAWorkspaceKeepsTheEngineItWasCreatedWith(t *testing.T) {
	t.Parallel()
	bwrapEngine(t)
	s := start(t)
	c := s.client()
	c.waitForConversation("general")

	// The gateway's registry is the record the interface writes through; a second workspace
	// with its own engine is what the create form posts.
	general := c.createWorkspaceWith("wrapped", map[string]any{"engine": "bwrap"})
	found := false
	for _, workspace := range general.Workspaces {
		if workspace.Name != "wrapped" {
			continue
		}
		if workspace.Engine != "bwrap" {
			t.Fatalf("the listing should say bwrap, got %+v", workspace)
		}
		found = true
	}
	if !found {
		t.Fatal("the created workspace is not in the listing")
	}
}

// One install, two engines: the general workspace keeps the install's default and a new one
// runs on bwrap, in the same gateway. This is what the per-workspace field buys, and it is the
// case that would break if engine selection were still an install-wide switch.
func TestOneInstallRunsTwoEngines(t *testing.T) {
	t.Parallel()
	workspaceResources(t) // the install default is host, so a bwrap workspace is a real choice
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("no bubblewrap on this machine")
	}
	s := start(t)
	c := s.client()
	c.waitForConversation("general")

	if engine := c.state("general").Sandbox.Engine; engine != "host" {
		t.Fatalf("the general workspace should follow the install default, got %q", engine)
	}
	c.createWorkspaceWith("wrapped", map[string]any{"engine": "bwrap"})
	wrapped := c.state("wrapped").Sandbox
	if wrapped.Engine != "bwrap" {
		t.Fatalf("the wrapped workspace should run on bwrap, got %+v", wrapped)
	}
	if !wrapped.Isolated || !wrapped.Tools {
		t.Fatalf("a bwrap workspace is isolated and its tools run, got %+v", wrapped)
	}
	// The default is unchanged by one workspace's choice.
	if engine := c.state("general").Sandbox.Engine; engine != "host" {
		t.Fatalf("creating a bwrap workspace changed the general one to %q", engine)
	}
}
