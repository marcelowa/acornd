package e2e

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
)

// With sandboxes forced off, the product has to take its refusal path: no engine is installed
// or booted, and every tool says why it will not run. Auto-detecting the hypervisor instead
// made one command boot microVMs on this machine and refuse in CI, which is the divergence
// the switch exists to remove.
//
// The test skips when the switch is not set, because then the sandbox tier is meant to run:
// `make test` sets it, `make test-sandbox` does not.
func TestForcedNoSandboxRefusesInsteadOfFallingBack(t *testing.T) {
	if os.Getenv("ACORNFALL_NO_SANDBOX") == "" {
		t.Skip("ACORNFALL_NO_SANDBOX=1 is what this asserts; make test sets it")
	}
	t.Parallel()
	notSandbox(t)
	s := start(t)
	c := s.client()
	c.waitForConversation("general")

	got := c.state("general")
	if got.Sandbox.Tools {
		t.Errorf("tools must not be available when the sandbox tier is forced off, got %+v", got.Sandbox)
	}
	if got.Sandbox.Problem == "" {
		t.Error("the refusal must say why, or a client cannot explain a missing tool")
	}
}

// What a microVM workspace may reach, proved against the engine rather than against our own argv:
// the arguments are the decision, and these are the two cases that decision exists for.
//
// The engine refuses private networks by default and includes a DNS allowance that disappears the
// moment any rule is written — so "public only" used to be the engine's default rather than ours,
// and the grant used to be a no-op that the dialog still reported as allowed. Both directions are
// asserted here, because only the pair says the policy is ours.
func TestAMicroVMSandboxCannotReachPrivateNetworks(t *testing.T) {
	t.Parallel()
	maybeSandbox(t)
	address := hostPrivateIPv4(t)
	if address == "" {
		t.Skip("this machine has no private IPv4 address to aim at")
	}
	listener, port := listenOn(t, address)
	defer listener.Close()

	s := start(t, withScript(privateProbeRule(address, port, "probed-private")))
	c := s.client()
	root := c.waitForConversation("general")
	c.prompt("general", root, "reach the private network")
	output := c.waitForText("general", root, "probed-private").lastToolResult()

	if want := fmt.Sprintf("%s:%d closed", address, port); !strings.Contains(output, want) {
		t.Fatalf("the microVM reached this machine's private address, want %q in %q", want, output)
	}
	// Name resolution is part of the same policy: a rule set that names public routes and forgets
	// the resolver leaves a sandbox that resolves nothing, which is how the first attempt at this
	// failed.
	if dns := fmt.Sprintf("%s:%d closed", address, port); dns == output {
		t.Logf("the public path is not asserted here: this test is about the refusal")
	}
}

// And with the grant, the same sandbox reaches it: the dialog says "private networks: allowed" and
// the engine now agrees.
func TestAMicroVMSandboxWithTheGrantReachesPrivateNetworks(t *testing.T) {
	t.Parallel()
	maybeSandbox(t)
	address := hostPrivateIPv4(t)
	if address == "" {
		t.Skip("this machine has no private IPv4 address to aim at")
	}
	listener, port := listenOn(t, address)
	defer listener.Close()

	s := start(t, withScript(privateProbeRule(address, port, "probed-private")))
	c := s.client()
	// The grant is recorded before the first prompt, so the sandbox that runs the probe is the one
	// built with it: a grant applied later is a restart, and this is about the policy itself.
	c.post("/api/workspaces/general/update", map[string]any{"private": true}, nil)
	root := c.waitForConversation("general")
	c.prompt("general", root, "reach the private network")
	output := c.waitForText("general", root, "probed-private").lastToolResult()

	if want := fmt.Sprintf("%s:%d open", address, port); !strings.Contains(output, want) {
		t.Fatalf("the grant was recorded and the sandbox still cannot reach it, want %q in %q", want, output)
	}
}

// listenOn accepts on this machine's private address, so a sandbox that reaches it is reaching the
// host rather than something inside itself.
func listenOn(t *testing.T, address string) (net.Listener, int) {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(address, "0"))
	if err != nil {
		t.Fatalf("listening on %s: %v", address, err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	return listener, listener.Addr().(*net.TCPAddr).Port
}

// privateProbeRule is the same probe the bwrap test uses: a bash tool that tries to open a socket
// to one address and reports what happened, in words the test can look for.
func privateProbeRule(address string, port int, reply string) scriptRule {
	return scriptRule{
		Match: "reach the private network",
		Tools: []scriptTool{{Name: "bash", Args: map[string]any{
			"command": fmt.Sprintf(`try() { timeout 3 bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null && echo "$1:$2 open" || echo "$1:$2 closed"; }; `+
				`try %s %d`, address, port),
		}}},
		Reply: reply,
	}
}
