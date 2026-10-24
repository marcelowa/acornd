package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// publishedRelease serves one release with a *real* executable as its binary: the updater runs
// what it staged and insists it names the tag, which is the check that a download is a build and
// not a file that happens to match a checksum.
func publishedRelease(t *testing.T, tag string) string {
	t.Helper()
	binary := []byte("#!/bin/sh\necho " + tag + "\n")
	worker := []byte("worker tarball bytes")
	sum := func(payload []byte) string {
		hash := sha256.Sum256(payload)
		return hex.EncodeToString(hash[:])
	}
	base := ""
	mux := http.NewServeMux()
	// The manager derives its API base from the mirror URL by cutting at "/releases/", so the paths
	// arrive with the repository in front of them: "/binary" here is "/repos/owner/repo/..." there.
	mux.HandleFunc("/repos/owner/repo/binary", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(binary) })
	mux.HandleFunc("/repos/owner/repo/worker", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(worker) })
	mux.HandleFunc("/repos/owner/repo/checksums", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  acornfall-%s-%s\n%s  worker.tar.gz\n",
			sum(binary), runtime.GOOS, runtime.GOARCH, sum(worker))
	})
	// One release, answered both ways: `latest` for the check, and by tag for the apply that named
	// the release the picker chose.
	one := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"tag_name":%q,"name":%q,"published_at":"2026-10-20T10:00:00Z","assets":[
			{"id":0,"name":%q,"size":%d,"browser_download_url":%q},
			{"id":0,"name":"worker.tar.gz","size":%d,"browser_download_url":%q},
			{"id":0,"name":"sha256sums.txt","size":0,"browser_download_url":%q}]}`,
			tag, tag,
			"acornfall-"+runtime.GOOS+"-"+runtime.GOARCH, len(binary), base+"/repos/owner/repo/binary",
			len(worker), base+"/repos/owner/repo/worker", base+"/repos/owner/repo/checksums")
	}
	mux.HandleFunc("/repos/owner/repo/releases/latest", one)
	mux.HandleFunc("/repos/owner/repo/releases/tags/v9.9.9", one)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	base = server.URL
	return server.URL + "/repos/owner/repo/releases/latest"
}

// An update installed through the running gateway: the screen's apply, then the rollback that
// undoes it. The gateway runs from a copy of the binary here, because an update replaces the binary
// that ran and the suite's own build must not be the one swapped out from under it.
func TestTheGatewayInstallsAndRollsBackAnUpdate(t *testing.T) {
	t.Parallel()
	tag := "v9.9.9"
	s := start(t, withOwnBinary(), withUpdateURL(publishedRelease(t, tag)), withSetup(func(t *testing.T, s *system) {
		// What this build runs. A development build has nothing to compare a release against.
		if err := os.WriteFile(filepath.Join(s.DataDir, "version"), []byte("v0.1.0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}))
	c := s.client()
	installed := filepath.Join(s.DataDir, "acornfall")

	// The answer is the status after the swap: the version running is the new one, and a restart is
	// owed. `latest` stays what the last *check* found — applying a tag from the picker must not
	// rewrite the answer to a different question.
	var applied struct {
		RestartRequired bool   `json:"restartRequired"`
		Current         string `json:"current"`
	}
	c.post("/api/update/apply", map[string]any{"tag": tag}, &applied)
	if applied.Current != tag {
		t.Fatalf("apply answered %+v", applied)
	}
	if !applied.RestartRequired {
		t.Error("a swapped binary needs a restart, and the answer has to say so")
	}

	// The files are what an update leaves: the new build in place, the old one beside it, and the
	// version the new build names.
	body, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), tag) {
		t.Errorf("the installed binary does not name the release: %q", body)
	}
	previous, err := os.ReadFile(installed + ".bak")
	if err != nil {
		t.Fatalf("the old binary should be kept beside the new one: %v", err)
	}
	if strings.Contains(string(previous), tag) {
		t.Error("the backup is the new build, not the one it replaced")
	}
	if version, err := os.ReadFile(filepath.Join(s.DataDir, "version")); err != nil || strings.TrimSpace(string(version)) != tag {
		t.Errorf("version = %q (%v)", version, err)
	}

	// A second apply while a restart is pending is refused: it would move the new build aside as
	// the backup and strand the rollback.
	if status, body := c.do("POST", "/api/update/apply", map[string]any{"tag": tag}); status != 409 {
		t.Errorf("a second apply: %d %s", status, body)
	}

	// Rolling back puts the running build back where it was.
	var rolled struct {
		Current string `json:"current"`
	}
	c.post("/api/update/rollback", nil, &rolled)
	if rolled.Current == tag {
		t.Errorf("after a rollback the version is still %q", rolled.Current)
	}
	restored, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(previous) {
		t.Error("the rollback should put the old binary back")
	}
}
