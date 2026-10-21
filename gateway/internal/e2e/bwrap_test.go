package e2e

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bubblewrap engine, end to end: a workspace that chose it says so, and its tools run inside
// the workspace's own image — root of its own, writable, with none of the machine's own files
// reachable.
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
	for _, want := range []string{"uid:0", "os:debian", "image-writable"} {
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
	bwrapAvailable(t)
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

// The workspace's rootfs, end to end: an image is fetched once, each workspace gets its own copy
// of it, and an install inside the sandbox is still there for the next command while the host
// keeps its own tools untouched.
//
// It needs a registry, so it does not run by default: `make test` stays offline and fast.
// ACORNFALL_IMAGE_TEST=1 runs it, and it costs one `debian:stable-slim` pull per test home.
func TestBwrapWorkspaceInstallsIntoItsOwnImage(t *testing.T) {
	t.Parallel()
	bwrapEngine(t)
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
	root := c.waitForConversation("general")

	c.prompt("general", root, "install it")
	view := c.waitForText("general", root, "first-turn-done")
	first := view.lastToolResult()
	if !strings.Contains(first, "installed:/usr/bin/less") {
		t.Fatalf("the install should have landed inside the sandbox, got %q\ntranscript:\n%s", first, view.all())
	}
	if !strings.Contains(first, "13.") && !strings.Contains(first, "PRETTY_NAME") {
		t.Errorf("the command should see the image's own os-release, got %q", first)
	}

	// A second command is a second sandbox, and an install that did not survive it would make the
	// image a decoration.
	c.prompt("general", root, "still there")
	second := c.waitForText("general", root, "second-turn-done").lastToolResult()
	if !strings.Contains(second, "alive:yes") {
		t.Fatalf("the install did not survive the command that made it, got %q", second)
	}

	// Nothing of this reached the host: the workspace's root is its own folder.
	if _, err := os.Stat(filepath.Join(s.Home, "sandboxes", "bwrap")); err != nil {
		t.Fatalf("the engine's folder should hold the image and the workspace's copy: %v", err)
	}
}

// A sandbox must not reach the machine's own loopback. Two routes used to carry it there: pasta's
// automatic port forwarding, which mirrors every service the machine listens on into the sandbox's
// loopback — the acornfall API on 7777 among them, with no authentication in front of it — and the
// gateway translation that maps 10.0.2.2 onto the same loopback. Both are closed in the argv, and
// this fails if either comes back.
func TestABwrapSandboxCannotReachTheMachinesLoopback(t *testing.T) {
	t.Parallel()
	bwrapEngine(t)
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
			conn.Close()
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
	root := c.waitForConversation("general")
	c.prompt("general", root, "reach the host")
	output := c.waitForText("general", root, "probed").lastToolResult()

	for _, want := range []string{
		fmt.Sprintf("127.0.0.1:%d closed", port),
		fmt.Sprintf("10.0.2.2:%d closed", port),
	} {
		if !strings.Contains(output, want) {
			t.Errorf("the sandbox should not reach the machine, want %q in %q", want, output)
		}
	}
}

// A workspace with no private-networks grant cannot reach this machine's private address, while
// the public internet still works: egress carries the machine's routing table, so the boundary
// has to be drawn inside the sandbox.
//
// The machine's own address is the target: pasta reaches it before the refusal because that is
// what a NAT through the host does, and it is the closest thing to a tailnet peer a test can set
// up without one.
func TestABwrapSandboxCannotReachPrivateNetworks(t *testing.T) {
	t.Parallel()
	bwrapEngine(t)
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
			conn.Close()
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
	root := c.waitForConversation("general")
	c.prompt("general", root, "reach the private network")
	output := c.waitForText("general", root, "probed-private").lastToolResult()
	if want := fmt.Sprintf("%s:%d closed", address, port); !strings.Contains(output, want) {
		t.Fatalf("the sandbox reached this machine's private address, want %q in %q", want, output)
	}
}

// hostPrivateIPv4 is this machine's own private address, or empty when it has none: the ranges a
// sandbox refuses by default.
func hostPrivateIPv4(t *testing.T) string {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil || ip.To4() == nil || ip.IsLoopback() {
			continue
		}
		if ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return ip.String()
		}
	}
	return ""
}
