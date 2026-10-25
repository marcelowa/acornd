package control

import (
	"context"
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

// A proxy that is already there takes a rotation live, but a name the worker did not start with
// cannot reach a command: the placeholder is part of the environment the worker builds. The first
// proxy and the last one removed are topology changes on top of that, so both owe a restart.
func TestAFirstProxyAndANewNameNeedARestartButARotationDoesNot(t *testing.T) {
	t.Parallel()
	control := proxyControl(t)
	t.Cleanup(control.Close)
	ctx := context.Background()

	resolved := func(names ...string) sandboxSecrets {
		out := sandboxSecrets{}
		for _, name := range names {
			out.delivered = append(out.delivered, sandbox.Secret{
				Name: name, Value: "v", Hosts: []string{"api.example.com"}, Headers: true,
			})
			out.state = append(out.state, SandboxSecret{
				Name: name, Status: "delivered", Delivery: "substitute",
			})
		}
		return out
	}
	clearRestart := func() {
		restart := false
		control.recordSecrets(control.sandbox.Secrets, "", &restart)
	}

	if err := control.applyProxySecrets(ctx, resolved("GITHUB_TOKEN"), true); err != nil {
		t.Fatalf("first secret: %v", err)
	}
	if !control.SecretsRestart() {
		t.Fatal("the first proxy is a config the running sandbox and worker did not have")
	}

	// A rotation keeps the name, so it is a grant swap and nothing restarts.
	clearRestart()
	rotated := resolved("GITHUB_TOKEN")
	rotated.delivered[0].Value = "rotated"
	rotated.delivered[0].Hosts = []string{"api.github.com"}
	if err := control.applyProxySecrets(ctx, rotated, true); err != nil {
		t.Fatalf("rotation: %v", err)
	}
	if control.SecretsRestart() {
		t.Fatal("a rotation under the same proxy should stay live")
	}

	// A new name is not a rotation: the worker builds command environments from the names it
	// was started with, so it has to be started again.
	clearRestart()
	if err := control.applyProxySecrets(ctx, resolved("GITHUB_TOKEN", "CLOUDFLARE_TOKEN"), true); err != nil {
		t.Fatalf("second secret: %v", err)
	}
	if !control.SecretsRestart() {
		t.Fatal("a name the worker did not start with needs a restart")
	}

	// Removing the last proxy closes the listener and restarts the workspace that still points at
	// it, so a command already running cannot keep using the removed grants.
	clearRestart()
	if err := control.applyProxySecrets(ctx, sandboxSecrets{}, true); err != nil {
		t.Fatalf("remove every secret: %v", err)
	}
	if !control.SecretsRestart() {
		t.Fatal("removing the last proxy needs a restart")
	}
	if control.proxy != nil {
		t.Fatal("the listener should be closed with the last grant")
	}
}
