package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func TestRestartHintReadsWhatTheEngineSaid(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("this change needs a restart"), true},
		{errors.New("no changes were applied"), true},
		{errors.New("RESTART REQUIRED"), true},
		{errors.New("sandbox not found"), false},
		{errors.New(""), false},
	}
	for _, test := range cases {
		if got := restartHint(test.err); got != test.want {
			t.Errorf("restartHint(%q) = %v, want %v", test.err, got, test.want)
		}
	}
}

func TestFirstLineKeepsTheCause(t *testing.T) {
	cases := []struct{ in, want string }{
		{"one\ntwo\nthree", "one"},
		{"only a line", "only a line"},
		{"", ""},
	}
	for _, test := range cases {
		if got := firstLine(test.in); got != test.want {
			t.Errorf("firstLine(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestHostsCompareInTheEnginesVocabulary(t *testing.T) {
	pattern := func(shape map[string]string) json.RawMessage {
		encoded, err := json.Marshal(shape)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	cases := []struct {
		name string
		raw  []json.RawMessage
		want []string
		same bool
	}{
		{"exact, in any case", []json.RawMessage{pattern(map[string]string{"exact": "api.example.com"})}, []string{"API.example.com"}, true},
		{"a wildcard is not an exact host", []json.RawMessage{pattern(map[string]string{"wildcard": "*.example.com"})}, []string{"api.example.com"}, false},
		{"one wildcard for another", []json.RawMessage{pattern(map[string]string{"wildcard": "*.example.com"})}, []string{"*.example.com"}, true},
		{"any is a shape of its own", []json.RawMessage{pattern(map[string]string{"any": "true"})}, []string{"example.com"}, false},
		{"nothing against nothing", nil, nil, true},
		{"a pattern this build cannot read is a change", []json.RawMessage{[]byte(`"not-an-object"`)}, nil, false},
	}
	for _, test := range cases {
		if got := sameHosts(test.raw, test.want); got != test.same {
			t.Errorf("%s: sameHosts = %v, want %v", test.name, got, test.same)
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

func TestStateDirIsTheOneItWasGiven(t *testing.T) {
	dir := t.TempDir()
	// The engine keeps its own folder under whatever it is given, so two engines never share one.
	if got := NewBwrap("data").WithStateDir(dir).StateDir(); got != filepath.Join(dir, "bwrap") {
		t.Errorf("state dir = %q, want %q", got, filepath.Join(dir, "bwrap"))
	}
	if got := EngineVersion(); !strings.Contains(got, ".") {
		t.Errorf("the engine version is a release: %q", got)
	}
}

func TestBwrapRefusesAPolicyWithNoWorkspace(t *testing.T) {
	err := NewBwrap(t.TempDir()).validatePolicy(Policy{})
	if err == nil {
		t.Fatal("a policy with no workspace should be refused")
	}
	// And one whose folder is not there: the sandbox mounts it, so a missing folder is a
	// sandbox that would start empty.
	err = NewBwrap(t.TempDir()).validatePolicy(Policy{Workspace: filepath.Join(t.TempDir(), "gone")})
	if err == nil || !strings.Contains(err.Error(), "no workspace folder") {
		t.Fatalf("a missing folder should be named, got %v", err)
	}
}
