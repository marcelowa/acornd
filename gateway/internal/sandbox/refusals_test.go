package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The refusals. Each of these is a sentence a person reads at the moment something did not work,
// so each is tested as a sentence: the branch is not the point, what it says is.

func TestAFetchSaysWhyTheRegistryRefused(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   string
	}{
		{"a private image", http.StatusUnauthorized, "it may be private"},
		{"a forbidden image", http.StatusForbidden, "it may be private"},
		{"an image that is not there", http.StatusNotFound, "no image"},
		{"anything else", http.StatusInternalServerError, "answered 500"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			host := strings.TrimPrefix(server.URL, "http://")
			registry := &registry{host: host, client: server.Client()}
			_, err := fetchManifest(context.Background(), registry, "token", imageReference{
				host: host, repository: "library/base", tag: "1",
			})
			if err == nil {
				t.Fatalf("a %d should be an error", test.status)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("the refusal should say %q, got %q", test.want, err)
			}
		})
	}
}

func TestAManifestThisBuildCannotUseIsNamed(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"no layers at all", `{"schemaVersion":2}`, "has no layers"},
		{
			"an index with no build for this machine",
			`{"schemaVersion":2,"manifests":[{"digest":"sha256:x","platform":{"os":"plan9","architecture":"mips"}}]}`,
			"publishes no",
		},
		{"not JSON", `{`, "not JSON this build can read"},
	}
	osName, arch := platform()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := chooseManifest(context.Background(), &registry{host: "fake"}, "", imageReference{
				host: "fake", repository: "library/base", tag: "1",
			}, []byte(test.body))
			if err == nil {
				t.Fatalf("%s should be an error", test.name)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("want %q, got %q", test.want, err)
			}
			if test.name == "an index with no build for this machine" && !strings.Contains(err.Error(), osName+"/"+arch) {
				t.Errorf("the refusal should name this machine's platform, got %q", err)
			}
		})
	}
}

func TestALayerCannotWriteOutsideTheRoot(t *testing.T) {
	dest := t.TempDir()
	// An entry is rooted at dest, so a leading slash is not an escape: it names /etc/passwd
	// inside the root. A `..` is the escape, and it is the one that has to be refused.
	for _, name := range []string{"../escape", "a/../../escape", "a/../../escape/x"} {
		err := applyEntry(dest, &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644}, bytes.NewReader(nil))
		if err == nil {
			t.Errorf("%q should be refused", name)
		}
	}
	// The root itself is not a member to write.
	if err := applyEntry(dest, &tar.Header{Name: "./", Typeflag: tar.TypeDir}, bytes.NewReader(nil)); err != nil {
		t.Errorf("the root is not an escape: %v", err)
	}
}

func TestAZstdLayerIsRefusedRatherThanSkipped(t *testing.T) {
	err := unpackLayer(bytes.NewReader(nil), "application/zstd", t.TempDir())
	if err == nil {
		t.Fatal("a zstd layer should be refused")
	}
	if !strings.Contains(err.Error(), "zstd") {
		t.Errorf("the refusal should name the compression, got %q", err)
	}
}

func TestShortDigestKeepsAPrefix(t *testing.T) {
	digest := sha256.Sum256([]byte("layer"))
	got := shortDigest(digest[:])
	if len(got) != 12 {
		t.Errorf("shortDigest is %d characters, want 12: %q", len(got), got)
	}
	if strings.HasPrefix(got, "sha256:") {
		t.Errorf("the prefix is noise in a log line: %q", got)
	}
}

func TestFirstLineKeepsTheCause(t *testing.T) {
	cases := []struct{ in, want string }{
		{"one\ntwo\nthree", "one"},
		{"only a line", "only a line"},
		{"", ""},
	}
	for _, test := range cases {
		if got := FirstLine(test.in); got != test.want {
			t.Errorf("FirstLine(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestTheEnvironmentEngineTakesEverythingLive(t *testing.T) {
	// There is no guest to keep a value out of, so every secret is in the worker's own process
	// and every change to one needs a new process. These are the answers that make that true.
	if err := (&Host{}).Egress(); err != nil {
		t.Errorf("the host can always reach the network: %v", err)
	}
	change, err := (&Host{}).SecretsChanged(context.Background(), "w", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !change.RestartRequired {
		t.Error("an environment cannot be changed under a running process")
	}
	if !(&Host{}).SecretEnvInWorker() {
		t.Error("the host engine delivers through the worker's environment")
	}
}
