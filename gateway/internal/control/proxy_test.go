package control

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/sandbox"
	"github.com/acornfall/acornfall/gateway/internal/secretsproxy"
)

// proxyControl is a control with nothing running: the proxy's own tests drive the traffic, and
// what is checked here is the wiring — where it listens, what it is handed, and when it exists.
func proxyControl(t *testing.T) *Control {
	t.Helper()
	return New(Options{
		Workspace:   Workspace{Name: "w", Path: t.TempDir()},
		DataDir:     t.TempDir(),
		StoragePath: filepath.Join(t.TempDir(), "storage"),
	})
}

// A workspace whose engine substitutes gets a proxy on the port its sandbox will forward, handed
// the vault's grants, with a CA the sandbox's bundle can be built from.
func TestASubstituteEngineGetsAProxyForItsSecrets(t *testing.T) {
	t.Parallel()
	control := proxyControl(t)
	delivered := []sandbox.Secret{{
		Name: "GITHUB_TOKEN", Value: "a-real-value", Hosts: []string{"api.github.com"}, Headers: true,
	}}

	proxy, err := control.startSecretsProxy(sandbox.DeliverySubstitute, delivered)
	if err != nil {
		t.Fatalf("start the proxy: %v", err)
	}
	if proxy == nil {
		t.Fatal("a substitute engine with secrets should have a proxy")
	}
	// The port is derived from the workspace, because the sandbox's argv carries it: a gateway that
	// restarts has to bind the one the sandbox it adopts was booted with.
	if want := fmt.Sprintf("127.0.0.1:%d", proxyPort(control.opts.Workspace.Path)); proxy.Addr() != want {
		t.Errorf("the proxy listens at %s, want %s", proxy.Addr(), want)
	}

	policy := control.secretProxyPolicy(proxy)
	parsed, err := url.Parse(policy.URL)
	if err != nil {
		t.Fatalf("the policy's URL is not a URL: %v", err)
	}
	if parsed.Host != proxy.Addr() {
		t.Errorf("the URL names %s, and the proxy is at %s", parsed.Host, proxy.Addr())
	}
	password, _ := parsed.User.Password()
	if password != proxy.Token() {
		t.Error("the URL should carry the proxy's own credential")
	}
	if policy.CAFile != secretsproxy.CAPath(filepath.Join(control.opts.DataDir, "proxy")) {
		t.Errorf("the CA the engine installs is %s, which is not the install's CA", policy.CAFile)
	}

	// A second start is the rotation path: the same listener, with the new grants.
	again, err := control.startSecretsProxy(sandbox.DeliverySubstitute, delivered)
	if err != nil {
		t.Fatalf("restart the proxy: %v", err)
	}
	if again != proxy {
		t.Error("a running proxy should be reused, not replaced")
	}
}

// Nothing to serve, nothing listening: an environment engine substitutes host-side or not at all,
// and a vault that resolved to nothing has no grant to hand over.
func TestAProxyIsOnlyStartedWhenThereIsSomethingToSubstitute(t *testing.T) {
	t.Parallel()
	control := proxyControl(t)
	delivered := []sandbox.Secret{{Name: "K", Value: "v", Hosts: []string{"api.example.com"}, Headers: true}}

	for _, one := range []struct {
		why      string
		delivery sandbox.Delivery
		secrets  []sandbox.Secret
	}{
		{"an environment engine", sandbox.DeliveryEnvironment, delivered},
		{"no secrets", sandbox.DeliverySubstitute, nil},
	} {
		proxy, err := control.startSecretsProxy(one.delivery, one.secrets)
		if err != nil {
			t.Errorf("%s: %v", one.why, err)
		}
		if proxy != nil {
			t.Errorf("%s: there is nothing to serve, so nothing should listen", one.why)
		}
	}
}

// Closing the workspace closes the proxy. A listener left behind would answer the next sandbox on
// a port this workspace no longer owns, with grants it no longer has.
func TestClosingTheWorkspaceStopsItsProxy(t *testing.T) {
	t.Parallel()
	control := proxyControl(t)
	proxy, err := control.startSecretsProxy(sandbox.DeliverySubstitute, []sandbox.Secret{{
		Name: "K", Value: "v", Hosts: []string{"api.example.com"}, Headers: true,
	}})
	if err != nil || proxy == nil {
		t.Fatalf("start the proxy: %v", err)
	}
	addr := proxy.Addr()

	control.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("the proxy at %s still answers after the workspace closed", addr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A workspace with its network off has no pasta, and pasta is what carries the proxy. Delivering a
// placeholder with nothing behind it is a command that fails with "connection refused" for a reason
// nothing in the transcript explains, so the secrets are withheld and the state says why.
// proxyManager is the bwrap engine's manager, which is the one that asks control for a proxy.
func proxyManager(t *testing.T, control *Control) *sandbox.Manager {
	t.Helper()
	tier := sandbox.NewTier(t.TempDir(), "", nil)
	manager, err := tier.For("bwrap")
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestAWorkspaceWithNoNetworkGetsNoProxyAndSaysWhy(t *testing.T) {
	t.Parallel()
	control := proxyControl(t)
	resolved := sandboxSecrets{delivered: []sandbox.Secret{{
		Name: "GITHUB_TOKEN", Value: "v", Hosts: []string{"api.github.com"}, Headers: true,
	}}}
	policy := sandbox.Policy{Workspace: control.opts.Workspace.Path, Egress: false}
	policy.Secrets = resolved.delivered

	control.applyProxyToPolicy(&policy, proxyManager(t, control), sandbox.DeliverySubstitute, &resolved)

	if policy.SecretProxy != nil {
		t.Error("a workspace with no network has no proxy to reach")
	}
	if len(policy.Secrets) != 0 {
		t.Error("a secret with nothing to use it on should not reach the prompt")
	}
	if !strings.Contains(resolved.problem, "network off") {
		t.Errorf("the state should say why: %q", resolved.problem)
	}
}
