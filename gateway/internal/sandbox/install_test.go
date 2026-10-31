package sandbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Installing the engine is the step a fresh machine takes before anything else works, and every
// refusal in it has to be an explanation: the release has no build for this machine, the download
// is not the file the release published, or the folder cannot be written.

func serveChecksums(t *testing.T, lines string) (*http.Client, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(lines))
	}))
	t.Cleanup(server.Close)
	return server.Client(), server.URL
}

// An artifact missing from the release's checksum file is an error rather than a reason to skip
// verification: the file covers every platform, so a name that is not in it is a release that did
// not build here.
func TestAnArtifactMissingFromTheChecksumsIsRefused(t *testing.T) {
	client, url := serveChecksums(t, "abc123  something-else-x86_64\n")
	_, err := fetchChecksum(context.Background(), client, url+"/checksums.sha256", "acornd-linux-amd64")
	if err == nil {
		t.Fatal("an artifact with no checksum should be refused")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("it should be the unavailable kind of error: %v", err)
	}
	if !strings.Contains(err.Error(), "acornd-linux-amd64") {
		t.Errorf("the refusal should name the artifact: %v", err)
	}
}

// A checksum line carries a star for a binary transfer, and the hash is compared lower-cased: a
// registry that writes it in capitals still means the same digest.
func TestAChecksumLineIsReadTheWayTheFileWritesIt(t *testing.T) {
	client, url := serveChecksums(t, "ABC123DEF  *acornd-linux-amd64\n")
	got, err := fetchChecksum(context.Background(), client, url+"/checksums.sha256", "acornd-linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	if got != "abc123def" {
		t.Errorf("checksum = %q, want it lower-cased and without the star", got)
	}
}

// A download that is not a 200 is an unavailable engine, not a crash: the status is in the words,
// so a 404 for a release that was pulled is something a person can act on.
func TestADownloadThatIsNotOKIsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer server.Close()
	_, err := fetch(context.Background(), server.Client(), server.URL+"/artifact")
	if err == nil {
		t.Fatal("a 404 should be an error")
	}
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "404") {
		t.Errorf("answered %v", err)
	}
}

// A runner that can run but not carry an environment is a different interface, and the engine
// falls back to plain Run rather than failing: an older runner still works, with the environment
// merged the only way it knows.
func TestARunnerWithoutAnEnvironmentStillRuns(t *testing.T) {
	runner := &plainRunner{}
	engine := &Microsandbox{runner: runner, base: "http://registry.invalid", dataDir: t.TempDir()}
	got, err := engine.runEnv(context.Background(), []string{"EXTRA=1"}, "version")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ran" {
		t.Errorf("runEnv = %q", got)
	}
	if len(runner.args) != 1 || runner.args[0] != "version" {
		t.Errorf("args = %v", runner.args)
	}
}

// An engine that is not installed says so before trying to run a binary that is not there.
func TestAnEngineThatIsNotInstalledSaysSo(t *testing.T) {
	engine := &Microsandbox{base: "http://registry.invalid", dataDir: t.TempDir()}
	if _, err := engine.runEnv(context.Background(), nil, "version"); err == nil {
		t.Fatal("a missing engine should not be run")
	} else if !errors.Is(err, ErrUnavailable) {
		t.Errorf("it should be the unavailable kind: %v", err)
	}
}

// plainRunner implements Runner and nothing else, which is what the fallback is for.
type plainRunner struct{ args []string }

func (p *plainRunner) Run(_ context.Context, args ...string) (string, error) {
	p.args = args
	return "ran", nil
}

// A staging folder that cannot be made is reported before anything is downloaded: the failure comes
// from the disk rather than from a download that then has nowhere to land.
func TestAStagingFolderThatCannotBeMadeIsReported(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, url := serveChecksums(t, "abc  artifact\n")
	engine := &Microsandbox{
		base:    url,
		dataDir: t.TempDir(),
		client:  client,
	}
	if err := engine.stage(context.Background(), "artifact", filepath.Join(blocked, "staging")); err == nil {
		t.Fatal("a staging folder under a file should be reported")
	}
}
