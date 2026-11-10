package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testCA = "-----BEGIN CERTIFICATE-----\nthe install CA\n-----END CERTIFICATE-----\n"

func testProxyPolicy(t *testing.T, url, ca string) *Policy {
	t.Helper()
	return &Policy{SecretProxy: &SecretProxy{URL: url, CAFile: ca}}
}

// The bundle a sandbox trusts is the image's own roots followed by this install's CA. Binding only
// ours would work for a secret and break every package install; binding only the image's is a
// proxy nobody trusts.
func TestSecretProxyForBuildsTheBundleAndPort(t *testing.T) {
	rootfs := t.TempDir()
	imageBundle := filepath.Join(rootfs, "etc", "ssl", "certs", "ca-certificates.crt")
	if err := os.MkdirAll(filepath.Dir(imageBundle), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imageBundle, []byte("the image roots\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, []byte(testCA), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	proxy, err := SecretProxyFor(testProxyPolicy(t, "http://acornd:token@127.0.0.1:18443", ca), workspace, rootfs)
	if err != nil {
		t.Fatalf("SecretProxyFor: %v", err)
	}
	if proxy.URL != "http://acornd:token@127.0.0.1:18443" || proxy.Port != 18443 {
		t.Fatalf("proxy = %+v, want the policy's URL and port", proxy)
	}
	if proxy.Bundle != GuestCABundle {
		t.Errorf("bundle = %q, want %q", proxy.Bundle, GuestCABundle)
	}
	raw, err := os.ReadFile(proxy.BundleFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "the image roots") || !strings.Contains(string(raw), "the install CA") {
		t.Errorf("the bundle should carry both roots:\n%s", raw)
	}
}

func TestSecretProxyForIsNilWithoutAProxy(t *testing.T) {
	proxy, err := SecretProxyFor(&Policy{}, t.TempDir(), "")
	if err != nil || proxy != nil {
		t.Fatalf("no proxy = %+v, %v", proxy, err)
	}
}

func TestSecretProxyForRefusesAURLWithoutAPort(t *testing.T) {
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, []byte(testCA), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{
		"http://acornd:token@127.0.0.1",
		"http://acornd:token@127.0.0.1:not-a-port",
		"http://acornd:token@127.0.0.1:0",
		"http://acornd:token@127.0.0.1:70000",
	} {
		if _, err := SecretProxyFor(testProxyPolicy(t, url, ca), t.TempDir(), ""); err == nil {
			t.Errorf("%q should be refused", url)
		}
	}
}

func TestSecretProxyForNamesAnUnreadableCA(t *testing.T) {
	_, err := SecretProxyFor(testProxyPolicy(t, "http://acornd:token@127.0.0.1:18443", filepath.Join(t.TempDir(), "gone")), t.TempDir(), "")
	if err == nil || !strings.Contains(err.Error(), "CA") {
		t.Fatalf("an unreadable CA should be named, got %v", err)
	}
}

func TestImageRootsPrefersTheImagesBundle(t *testing.T) {
	rootfs := t.TempDir()
	path := filepath.Join(rootfs, "etc", "ssl", "certs", "ca-certificates.crt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("image roots"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := string(ImageRoots(rootfs)); got != "image roots" {
		t.Fatalf("ImageRoots = %q", got)
	}
}

func TestWriteTrustBundleSeparatesTheBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bundle.pem")
	if err := WriteTrustBundle(path, []byte("roots"), []byte("ca")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), "roots\nca\n"; got != want {
		t.Fatalf("bundle = %q, want %q", got, want)
	}
}

func TestProxyNeedsRestart(t *testing.T) {
	proxy := func(port string) *SecretProxy {
		return &SecretProxy{URL: "http://acornd:token@127.0.0.1:" + port}
	}
	running := &SandboxSecretProxy{Port: 18443}
	for _, tc := range []struct {
		name    string
		running *SandboxSecretProxy
		wanted  *SecretProxy
		want    bool
	}{
		{"neither", nil, nil, false},
		{"the first proxy", nil, proxy("18443"), true},
		{"the last proxy removed", running, nil, true},
		{"the same port is a rotation", running, proxy("18443"), false},
		{"a moved port", running, proxy("18444"), true},
		{"a URL with no port", running, proxy(""), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProxyNeedsRestart(tc.running, tc.wanted); got != tc.want {
				t.Fatalf("ProxyNeedsRestart = %v, want %v", got, tc.want)
			}
		})
	}
}
