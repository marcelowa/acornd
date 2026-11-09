package sandbox

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The loopback test first: issue #47 §5 says pasta's forwarding inside a youki container was
// never measured, and this is the row to run before the rest of the tier. pasta's defaults
// mirror every service the machine listens on into the sandbox's loopback; these flags close
// both routes, and the container's own 127.0.0.1 is not the machine's.
func TestYoukiSandboxCannotReachTheMachinesLoopback(t *testing.T) {
	driver, ctx := liveYouki(t)
	if err := driver.Egress(); err != nil {
		t.Fatalf("this machine cannot carry egress: %v", err)
	}
	_, port := listenOn(t, "127.0.0.1")
	workspace := filepath.Join(t.TempDir(), "loopback")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	name := Name(workspace)
	t.Cleanup(func() { _ = driver.Remove(context.Background(), name) })
	if err := driver.Create(ctx, Policy{Workspace: workspace, Image: "debian:stable-slim", Egress: true}, t.TempDir()); err != nil {
		t.Fatalf("create: %v\nlog: %s", err, driver.logTail(driver.logPath(name)))
	}
	config, err := driver.readConfig(name)
	if err != nil {
		t.Fatalf("the record: %v", err)
	}
	command := tryScript(tryProbe("127.0.0.1", port), tryProbe(sandboxGateway, port))
	output, code := runInSupervisor(t, config.Socket, command)
	if code != 0 {
		t.Fatalf("the probe did not run: %q (exit %d)", output, code)
	}
	for _, want := range []string{
		fmt.Sprintf("127.0.0.1:%d closed", port),
		fmt.Sprintf("%s:%d closed", sandboxGateway, port),
	} {
		if !strings.Contains(output, want) {
			t.Errorf("the sandbox should not reach the machine, want %q in %q", want, output)
		}
	}
}

// The private fence is a route the netns helper installs before the container starts: the
// machine's LAN, link-local and overlay ranges are refused unless the workspace has the grant.
func TestYoukiSandboxCannotReachPrivateNetworks(t *testing.T) {
	driver, ctx := liveYouki(t)
	if err := driver.Egress(); err != nil {
		t.Fatalf("this machine cannot carry egress: %v", err)
	}
	address := hostPrivateIPv4()
	if address == "" {
		t.Skip("this machine has no private IPv4 address to aim at")
	}
	_, port := listenOn(t, address)
	workspace := filepath.Join(t.TempDir(), "private")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	name := Name(workspace)
	t.Cleanup(func() { _ = driver.Remove(context.Background(), name) })
	if err := driver.Create(ctx, Policy{Workspace: workspace, Image: "debian:stable-slim", Egress: true}, t.TempDir()); err != nil {
		t.Fatalf("create: %v\nlog: %s", err, driver.logTail(driver.logPath(name)))
	}
	config, err := driver.readConfig(name)
	if err != nil {
		t.Fatalf("the record: %v", err)
	}
	output, code := runInSupervisor(t, config.Socket, tryScript(tryProbe(address, port)))
	if code != 0 {
		t.Fatalf("the probe did not run: %q (exit %d)", output, code)
	}
	if want := fmt.Sprintf("%s:%d closed", address, port); !strings.Contains(output, want) {
		t.Fatalf("the sandbox reached this machine's private address, want %q in %q", want, output)
	}
}

// listenOn is a listener on one address and the port a command inside the sandbox will try. It
// accepts and closes, so a connection that arrives is a success the test can see.
func listenOn(t *testing.T, address string) (net.Listener, int) {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(address, "0"))
	if err != nil {
		t.Fatalf("listening on %s: %v", address, err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return listener, listener.Addr().(*net.TCPAddr).Port
}

// tryScript is the probe the bwrap tier uses: bash's /dev/tcp with a timeout, one line per
// address saying whether it opened. The image has bash and coreutils.
func tryScript(probes ...string) string {
	return `try() { timeout 3 bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null && echo "$1:$2 open" || echo "$1:$2 closed"; }; ` +
		strings.Join(probes, "; ")
}

func tryProbe(address string, port int) string {
	return fmt.Sprintf("try %s %d", address, port)
}

// hostPrivateIPv4 is this machine's own private address, or empty when it has none: the ranges
// a sandbox refuses by default.
func hostPrivateIPv4() string {
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
