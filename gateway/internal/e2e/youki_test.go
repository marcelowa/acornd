package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The youki engine, end to end: a workspace that chose it runs its tools inside the workspace's
// own image — root of its own, writable, with none of the machine's own files reachable — and
// the command goes through the supervisor the container's init holds. It is the twin of the
// bwrap row; the policy file is the difference. See issue #47 R1 and R3.
func TestYoukiWorkspaceRunsItsToolsInNamespaces(t *testing.T) {
	t.Parallel()
	youkiEngine(t)
	s := start(t,
		withScript(
			scriptRule{
				Match: "who am i",
				Tools: []scriptTool{{Name: "bash", Args: map[string]any{
					// The root is the workspace's own copy of its image: root inside it, writable,
					// and the only root there is. The argv test pins the last part — there is no
					// bind of the machine's root at all.
					"command": `printf 'uid:%s' "$(id -u)"; printf ' marker' > made-in-the-sandbox.txt; ` +
						`printf 'os:%s' "$(. /etc/os-release && echo $ID)"; ` +
						`test -w /usr && echo image-writable || echo image-read-only`,
				}}},
				Reply: "the command ran",
			},
		),
	)
	c := s.client()
	root := c.waitForConversation("personal")

	general := c.state("personal")
	engine := general.Sandbox
	if engine.Engine != "youki" {
		t.Fatalf("the workspace should run on youki, got %+v", engine)
	}
	if !engine.Isolated || !engine.Tools {
		t.Fatalf("a youki workspace is isolated and its tools run, got %+v", engine)
	}

	c.prompt("personal", root, "who am i")
	got := c.waitForText("personal", root, "the command ran")
	output := got.lastToolResult()
	for _, want := range []string{"uid:0", "os:debian", "image-writable"} {
		if !strings.Contains(output, want) {
			t.Errorf("the command's output should contain %q, got %q", want, output)
		}
	}

	// The command wrote into the workspace through the sandbox, and the file is the user's own:
	// the workspace is bound at the path it already has, so the path is the same on both sides.
	written := filepath.Join(general.Workspace.Path, "made-in-the-sandbox.txt")
	if _, err := os.Stat(written); err != nil {
		t.Fatalf("the sandbox did not write into the workspace: %v", err)
	}
}
