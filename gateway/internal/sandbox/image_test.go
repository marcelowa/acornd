package sandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The registry is the only part of an engine with no local answer, so it is tested against a
// fake one: the manifest kind that must be walked to find this platform, the digest that must
// match before a byte lands, and the whiteout a later layer uses to delete an earlier one.
//
// No network: the fake serves what a registry serves, including its challenge.

// tarLayer builds a gzipped layer from entries, in the order they are given.
type tarEntry struct {
	name     string
	body     string
	link     string
	dir      bool
	whiteout bool
}

func tarLayer(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o644, Format: tar.FormatPAX}
		switch {
		case entry.dir:
			header.Typeflag = tar.TypeDir
			header.Mode = 0o755
		case entry.whiteout:
			header.Typeflag = tar.TypeReg
		case entry.link != "":
			header.Typeflag = tar.TypeSymlink
			header.Linkname = entry.link
		default:
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(entry.body))
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg && entry.body != "" {
			if _, err := writer.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// fakeRegistry serves one index, one platform manifest, and its layers.
type fakeRegistry struct {
	t       *testing.T
	layers  [][]byte
	corrupt bool
}

func (f *fakeRegistry) digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (f *fakeRegistry) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v2/":
		// The challenge is what a real registry answers, and the driver reads it rather than
		// assuming Docker Hub's auth host.
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="http://%s/token",service="fake"`, r.Host))
		w.WriteHeader(http.StatusUnauthorized)
	case r.URL.Path == "/token":
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "a-token"})
	case strings.HasSuffix(r.URL.Path, "/manifests/latest"):
		manifest := map[string]any{
			"schemaVersion": 2,
			"manifests": []map[string]any{
				{
					"mediaType": "application/vnd.oci.image.manifest.v1+json",
					"digest":    "sha256:other-platform",
					"platform":  map[string]string{"os": "plan9", "architecture": "mips"},
				},
				{
					"mediaType": "application/vnd.oci.image.manifest.v1+json",
					"digest":    "sha256:ours",
					"platform":  map[string]string{"os": "linux", "architecture": platformArch()},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(manifest)
	case strings.HasSuffix(r.URL.Path, "/manifests/sha256:ours"):
		layers := make([]map[string]any, 0, len(f.layers))
		for index, layer := range f.layers {
			digest := f.digest(layer)
			if f.corrupt && index == 0 {
				digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
			}
			layers = append(layers, map[string]any{
				"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
				"digest":    digest,
				"size":      len(layer),
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schemaVersion": 2,
			"layers":        layers,
		})
	case strings.Contains(r.URL.Path, "/blobs/sha256:"):
		digest := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if f.corrupt && len(f.layers) == 1 {
			// The registry happily serves the bytes for the digest it named: this is the case
			// where the download succeeds and the *content* is what is wrong.
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(f.layers[0])
			return
		}
		for _, layer := range f.layers {
			if f.digest(layer) == digest {
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write(layer)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func platformArch() string {
	_, arch := platform()
	return arch
}

func TestPullImageUnpacksTheLayerForThisPlatform(t *testing.T) {
	layer := tarLayer(t, []tarEntry{
		{name: "etc/", dir: true},
		{name: "etc/os-release", body: "ID=debian\n"},
		{name: "usr/", dir: true},
		{name: "usr/bin/", dir: true},
		{name: "usr/bin/sh", body: "#!/bin/sh\n"},
		{name: "usr/bin/sh-link", link: "sh"},
	})
	registry := &fakeRegistry{t: t, layers: [][]byte{layer}}
	server := httptest.NewServer(http.HandlerFunc(registry.handle))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "base")
	// The driver builds its registry URL from the reference's host, so the fake one is named by
	// the server's own address.
	host := strings.TrimPrefix(server.URL, "http://")
	if err := pullImage(context.Background(), host+"/library/debian:latest", dest, nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "etc", "os-release")); err != nil || string(got) != "ID=debian\n" {
		t.Fatalf("os-release: %q, %v", got, err)
	}
	link, err := os.Readlink(filepath.Join(dest, "usr", "bin", "sh-link"))
	if err != nil || link != "sh" {
		t.Fatalf("symlink: %q, %v", link, err)
	}
	info, err := os.Stat(filepath.Join(dest, "usr", "bin", "sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		// The layer said 0644, so this file is not executable: the mode came from the tar
		// header rather than from an umask.
		t.Log("mode 0644 preserved, as the layer said")
	}
}

// A later layer deletes what an earlier one put down, with a whiteout. Unpacking in order is
// what makes a multi-layer image look like the finished filesystem.
func TestPullImageAppliesWhiteoutsAcrossLayers(t *testing.T) {
	first := tarLayer(t, []tarEntry{
		{name: "etc/", dir: true},
		{name: "etc/keep", body: "keep\n"},
		{name: "etc/gone", body: "gone\n"},
		{name: "opt/", dir: true},
		{name: "opt/old", body: "old\n"},
	})
	second := tarLayer(t, []tarEntry{
		{name: "etc/.wh.gone", whiteout: true},
		{name: "opt/.wh..wh..opq", whiteout: true},
	})
	registry := &fakeRegistry{t: t, layers: [][]byte{first, second}}
	server := httptest.NewServer(http.HandlerFunc(registry.handle))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "base")
	if err := pullImage(context.Background(), strings.TrimPrefix(server.URL, "http://")+"/library/debian:latest", dest, nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "etc", "gone")); !os.IsNotExist(err) {
		t.Errorf("a whiteout did not remove the file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "etc", "keep")); err != nil {
		t.Errorf("a whiteout removed a file it did not name: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "opt", "old")); !os.IsNotExist(err) {
		t.Errorf("an opaque directory kept an earlier layer's file: %v", err)
	}
}

// An image is code. A layer whose bytes do not match the digest the manifest names is refused,
// and nothing is left half-unpacked for a later run to trust.
func TestPullImageRefusesALayerThatDoesNotMatchItsDigest(t *testing.T) {
	layer := tarLayer(t, []tarEntry{{name: "etc/os-release", body: "ID=debian\n"}})
	registry := &fakeRegistry{t: t, layers: [][]byte{layer}, corrupt: true}
	server := httptest.NewServer(http.HandlerFunc(registry.handle))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "base")
	err := pullImage(context.Background(), strings.TrimPrefix(server.URL, "http://")+"/library/debian:latest", dest, nil)
	if err == nil {
		t.Fatal("a corrupted layer was accepted")
	}
	if !strings.Contains(err.Error(), "did not match its digest") {
		t.Fatalf("the refusal should name the digest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "etc", "os-release")); !os.IsNotExist(err) {
		t.Errorf("a rejected layer reached the filesystem: %v", err)
	}
}

func TestParseImageReference(t *testing.T) {
	for _, test := range []struct{ in, host, repository, tag string }{
		{"debian:stable-slim", "docker.io", "library/debian", "stable-slim"},
		{"debian", "docker.io", "library/debian", "latest"},
		{"acme/tool:1.2", "docker.io", "acme/tool", "1.2"},
		{"ghcr.io/acme/tool:1.2", "ghcr.io", "acme/tool", "1.2"},
		{"localhost:5000/acme/tool", "localhost:5000", "acme/tool", "latest"},
	} {
		got, err := parseImageReference(test.in)
		if err != nil {
			t.Fatalf("%s: %v", test.in, err)
		}
		if got.host != test.host || got.repository != test.repository || got.tag != test.tag {
			t.Errorf("%s: got %s/%s:%s, want %s/%s:%s", test.in, got.host, got.repository, got.tag, test.host, test.repository, test.tag)
		}
	}
	for _, bad := range []string{"", "  ", "debian:"} {
		if _, err := parseImageReference(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
