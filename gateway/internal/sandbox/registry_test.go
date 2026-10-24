package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// The registry speaks a protocol this build has to match exactly, and every branch of it is an
// install that fails on a machine with no hypervisor to blame. These are the answers a registry
// actually gives: a manifest list, a manifest with no layers, another platform, and a plain
// refusal.

func registryOn(t *testing.T, handler http.HandlerFunc) *registry {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	// A registry on this machine speaks plain HTTP, which is the branch apiBase takes for a host
	// with a port: no scheme is guessed, and the test server is the host.
	return &registry{host: strings.TrimPrefix(server.URL, "http://"), client: server.Client()}
}

func TestAManifestListIsResolvedToThisPlatform(t *testing.T) {
	ours := imageManifest{Layers: []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	}{{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: "sha256:aa", Size: 1}}}
	body, err := json.Marshal(map[string]any{
		"manifests": []any{
			// An attestation first: it has no platform of its own, and asking for ours skips it.
			map[string]any{"digest": "sha256:attestation"},
			map[string]any{"digest": "sha256:other", "platform": map[string]string{"os": "plan9", "architecture": "mips"}},
			map[string]any{"digest": "sha256:ours", "platform": map[string]string{"os": runtime.GOOS, "architecture": runtime.GOARCH}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	asked := ""
	registry := registryOn(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		if !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") {
			t.Errorf("the manifest request should say what it can read: %q", r.Header.Get("Accept"))
		}
		_ = json.NewEncoder(w).Encode(ours)
	})

	context := context.Background()
	chosen, err := chooseManifest(context, registry, "a-token", imageReference{host: "registry", repository: "acme/tool", tag: "1"}, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(chosen.Layers) != 1 || chosen.Layers[0].Digest != "sha256:aa" {
		t.Errorf("chosen = %+v", chosen)
	}
	if !strings.HasSuffix(asked, "/manifests/sha256:ours") {
		t.Errorf("asked for %q, want this platform's digest", asked)
	}
}

// An image that publishes no build for this platform is refused by name, rather than installing
// another architecture's binaries and failing at exec.
func TestAnImageWithNoBuildForThisPlatformIsRefused(t *testing.T) {
	body := []byte(`{"manifests":[{"digest":"sha256:other","platform":{"os":"plan9","architecture":"mips"}}]}`)
	registry := registryOn(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("nothing should be fetched when no entry matches")
	})
	_, err := chooseManifest(context.Background(), registry, "", imageReference{host: "registry", repository: "acme/tool", tag: "1"}, body)
	if err == nil {
		t.Fatal("a platform nobody publishes should be refused")
	}
	if !strings.Contains(err.Error(), runtime.GOOS+"/"+runtime.GOARCH) {
		t.Errorf("the refusal should name the platform it wanted: %v", err)
	}
}

// A manifest that is not JSON, and one with no layers at all: the first is a registry this build
// cannot read, the second is an image that is nothing.
func TestAManifestThisBuildCannotReadIsRefused(t *testing.T) {
	registry := registryOn(t, func(w http.ResponseWriter, _ *http.Request) {})
	for _, one := range []struct {
		why  string
		body string
		says string
	}{
		{"not JSON", "not json at all", "not JSON"},
		{"no layers", `{"schemaVersion":2}`, "no layers"},
	} {
		_, err := chooseManifest(context.Background(), registry, "", imageReference{host: "registry", repository: "acme/tool", tag: "1"}, []byte(one.body))
		if err == nil {
			t.Errorf("%s: should be refused", one.why)
			continue
		}
		if !strings.Contains(err.Error(), one.says) {
			t.Errorf("%s: answered %v", one.why, err)
		}
	}
}

// A platform manifest that answers anything but 200 is refused with the registry's own status: a
// 401 here is a token that did not work, and saying so is the difference between a five-minute fix
// and an afternoon.
func TestAPlatformManifestThatIsRefusedSaysWhoRefused(t *testing.T) {
	registry := registryOn(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusUnauthorized)
	})
	_, err := registry.platformManifest(context.Background(), "stale-token", imageReference{host: "registry", repository: "acme/tool", tag: "1"}, "sha256:ours", runtime.GOOS, runtime.GOARCH)
	if err == nil {
		t.Fatal("a refused manifest should be reported")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("the refusal should carry the status: %v", err)
	}
}

// A layer that does not match the digest its manifest promised is never unpacked: this is the one
// check between a corrupted download and a rootfs that is subtly wrong.
func TestALayerIsCheckedAgainstItsDigest(t *testing.T) {
	payload := []byte("layer bytes")
	registry := registryOn(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("a layer download carries the token: %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write(payload)
	})
	layer := struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	}{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: "sha256:0000", Size: int64(len(payload))}

	err := applyLayer(context.Background(), registry, "a-token", imageReference{host: "registry", repository: "acme/tool", tag: "1"}, layer, t.TempDir(), nil)
	if err == nil {
		t.Fatal("a layer that does not match its digest should be refused")
	}
	if !strings.Contains(err.Error(), "digest") {
		t.Errorf("the refusal should name the digest: %v", err)
	}
}

// A layer the registry refuses is reported with the status: a 404 for a blob is a manifest that
// names something the registry does not have, which is a repository that has been rewritten.
func TestALayerTheRegistryRefusesIsReported(t *testing.T) {
	registry := registryOn(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such blob", http.StatusNotFound)
	})
	layer := struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	}{Digest: "sha256:aa", Size: 1}
	err := applyLayer(context.Background(), registry, "", imageReference{host: "registry", repository: "acme/tool", tag: "1"}, layer, t.TempDir(), nil)
	if err == nil {
		t.Fatal("a refused layer should be reported")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("the refusal should carry the status: %v", err)
	}
}

// The registry's base URL follows the host: Docker Hub is a different host name, a registry on this
// machine speaks plain HTTP, and everything else is HTTPS because an image is code.
func TestTheRegistryBaseFollowsTheHost(t *testing.T) {
	cases := map[string]string{
		"docker.io":      "https://registry-1.docker.io",
		"ghcr.io":        "https://ghcr.io",
		"127.0.0.1:5000": "http://127.0.0.1:5000",
		"localhost:5000": "http://localhost:5000",
	}
	for host, want := range cases {
		if got := (&registry{host: host}).apiBase(); got != want {
			t.Errorf("apiBase(%q) = %q, want %q", host, got, want)
		}
	}
}

var _ = fmt.Sprintf
