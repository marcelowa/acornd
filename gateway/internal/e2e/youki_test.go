package e2e

import (
	"fmt"
	"net"
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

// The machine's own loopback is not the workspace's. pasta's forwarding defaults mirror every
// listener into the sandbox's loopback and its gateway translation maps 10.0.2.2 onto the same
// surface; `-t none -u none -T none -U none --no-map-gw` closes both. This is the row issue #47
// §5 says to run first, because it was never measured with youki.
func TestAYoukiSandboxCannotReachTheMachinesLoopback(t *testing.T) {
	t.Parallel()
	youkiEngine(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening on the machine's loopback: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	s := start(t,
		withScript(
			scriptRule{
				Match: "reach the host",
				Tools: []scriptTool{{Name: "bash", Args: map[string]any{
					"command": fmt.Sprintf(
						`try() { timeout 3 bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null && echo "$1:$2 open" || echo "$1:$2 closed"; }; `+
							`try 127.0.0.1 %d; try 10.0.2.2 %d`, port, port),
				}}},
				Reply: "probed",
			},
		),
	)
	c := s.client()
	root := c.waitForConversation("personal")
	c.prompt("personal", root, "reach the host")
	output := c.waitForText("personal", root, "probed").lastToolResult()

	for _, want := range []string{
		fmt.Sprintf("127.0.0.1:%d closed", port),
		fmt.Sprintf("10.0.2.2:%d closed", port),
	} {
		if !strings.Contains(output, want) {
			t.Errorf("the sandbox should not reach the machine, want %q in %q", want, output)
		}
	}
}

// A youki workspace with no private-networks grant cannot reach this machine's private address:
// egress carries the machine's routing table, so the boundary is a route inside the sandbox.
func TestAYoukiSandboxCannotReachPrivateNetworks(t *testing.T) {
	t.Parallel()
	youkiEngine(t)
	address := hostPrivateIPv4(t)
	if address == "" {
		t.Skip("this machine has no private IPv4 address to aim at")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(address, "0"))
	if err != nil {
		t.Fatalf("listening on %s: %v", address, err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	s := start(t,
		withScript(
			scriptRule{
				Match: "reach the private network",
				Tools: []scriptTool{{Name: "bash", Args: map[string]any{
					"command": fmt.Sprintf(
						`try() { timeout 3 bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null && echo "$1:$2 open" || echo "$1:$2 closed"; }; `+
							`try %s %d`, address, port),
				}}},
				Reply: "probed-private",
			},
		),
	)
	c := s.client()
	root := c.waitForConversation("personal")
	c.prompt("personal", root, "reach the private network")
	output := c.waitForText("personal", root, "probed-private").lastToolResult()
	if want := fmt.Sprintf("%s:%d closed", address, port); !strings.Contains(output, want) {
		t.Fatalf("the sandbox reached this machine's private address, want %q in %q", want, output)
	}
}

// The workspace's rootfs, end to end: an install inside a youki workspace lands in the
// workspace's own copy of its image and is still there for the next command, while the host
// keeps its own tools untouched. The twin of the bwrap row (issue #47 R3).
func TestYoukiWorkspaceInstallsIntoItsOwnImage(t *testing.T) {
	t.Parallel()
	youkiEngine(t)
	s := start(t,
		withScript(
			scriptRule{
				Match: "install it",
				Tools: []scriptTool{{Name: "bash", Args: map[string]any{
					"command": `id -u; cat /etc/os-release | head -1; ` +
						`apt-get update -qq >/dev/null 2>&1 && apt-get install -y -qq less >/dev/null 2>&1; ` +
						`printf 'installed:%s' "$(command -v less)"`,
				}}},
				// The reply is a word the command does not contain: the stream's text is the whole
				// transcript, so a wait for "installed" would match the tool call's own arguments.
				Reply: "first-turn-done",
			},
			scriptRule{
				Match: "still there",
				Tools: []scriptTool{{Name: "bash", Args: map[string]any{
					"command": `command -v less && less --version | head -1 && printf 'alive:%s' "$(test -x /usr/bin/less && echo yes)"`,
				}}},
				Reply: "second-turn-done",
			},
		),
	)
	c := s.client()
	root := c.waitForConversation("personal")

	c.prompt("personal", root, "install it")
	view := c.waitForText("personal", root, "first-turn-done")
	first := view.lastToolResult()
	if !strings.Contains(first, "installed:/usr/bin/less") {
		t.Fatalf("the install should have landed inside the sandbox, got %q\ntranscript:\n%s", first, view.all())
	}
	if !strings.Contains(first, "13.") && !strings.Contains(first, "PRETTY_NAME") {
		t.Errorf("the command should see the image's own os-release, got %q", first)
	}

	// A second command is the same sandbox, and an install that did not survive it would make the
	// image a decoration.
	c.prompt("personal", root, "still there")
	second := c.waitForText("personal", root, "second-turn-done").lastToolResult()
	if !strings.Contains(second, "alive:yes") {
		t.Fatalf("the install did not survive the command that made it, got %q", second)
	}

	// Nothing of this reached the host: the workspace's root is its own copy in the engine store.
	if _, err := os.Stat(filepath.Join(s.DataDir, "sandboxes", "youki")); err != nil {
		t.Fatalf("the engine's folder should hold the image and the workspace's copy: %v", err)
	}
}
